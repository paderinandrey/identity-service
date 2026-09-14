package samlsso

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/redis/go-redis/v9"

	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/redisstore"
	"github.com/paderinandrey/identity-service/internal/session"
)

// --- in-memory identity.Store ---

type memStore struct {
	users      map[string]*identity.User // by id
	byEmail    map[string]string
	identities map[string]string // provider|subject -> user id
	lastSignIn map[string]int
}

func newMemStore() *memStore {
	return &memStore{
		users:      map[string]*identity.User{},
		byEmail:    map[string]string{},
		identities: map[string]string{},
		lastSignIn: map[string]int{},
	}
}

func (m *memStore) addUser(id, email string, active bool) *identity.User {
	u := &identity.User{ID: id, Email: email, Name: "User " + id, Active: active}
	m.users[id] = u
	m.byEmail[email] = id
	return u
}

// link is what provisioning does: it stores the IdP's stable id.
func (m *memStore) link(userID, subject string) {
	m.identities[identity.ProviderOkta+"|"+subject] = userID
}

func (m *memStore) FindByID(_ context.Context, id string) (*identity.User, error) {
	if u, ok := m.users[id]; ok {
		return u, nil
	}
	return nil, identity.ErrUserNotFound
}

func (m *memStore) FindByIdentity(_ context.Context, provider, subject string) (*identity.User, error) {
	if id, ok := m.identities[provider+"|"+subject]; ok {
		return m.users[id], nil
	}
	return nil, identity.ErrUserNotFound
}

func (m *memStore) FindActiveByEmail(_ context.Context, email string) (*identity.User, error) {
	if id, ok := m.byEmail[email]; ok && m.users[id].Active {
		return m.users[id], nil
	}
	return nil, identity.ErrUserNotFound
}

func (m *memStore) UpsertByEmail(_ context.Context, _, _ string) (*identity.User, error) {
	panic("not used")
}

func (m *memStore) TouchLastSignIn(_ context.Context, userID string) error {
	m.lastSignIn[userID]++
	return nil
}

// --- test IdP ---

type testIDP struct {
	server *httptest.Server
	idp    *saml.IdentityProvider
	// nameID is the persistent subject returned for every session — an
	// opaque id, the way Okta's user.id or a Keycloak user UUID looks.
	nameID string
	// email is emitted as the `email` attribute when non-empty.
	email string
	// spEntityID lets the IdP-initiated endpoint address our SP.
	spEntityID string
}

type staticSessionProvider struct{ t *testIDP }

func (s staticSessionProvider) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	return &saml.Session{
		ID:         "session-1",
		CreateTime: saml.TimeNow(),
		ExpireTime: saml.TimeNow().Add(time.Hour),
		Index:      "idx-1",
		NameID:     s.t.nameID,
		UserEmail:  s.t.email,
	}
}

// emailAttributeMaker adds an `email` attribute statement the way Okta's
// attribute statements or a Keycloak property mapper would. The assertion
// is signed after this hook, so the attribute is covered by the signature.
type emailAttributeMaker struct{}

func (emailAttributeMaker) MakeAssertion(req *saml.IdpAuthnRequest, s *saml.Session) error {
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, s); err != nil {
		return err
	}
	if s.UserEmail == "" {
		return nil
	}
	req.Assertion.AttributeStatements = append(req.Assertion.AttributeStatements, saml.AttributeStatement{
		Attributes: []saml.Attribute{{
			Name:       "email",
			NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic",
			Values:     []saml.AttributeValue{{Type: "xs:string", Value: s.UserEmail}},
		}},
	})
	return nil
}

type staticSPProvider struct {
	metadataURL string
	client      *http.Client
}

func (p staticSPProvider) GetServiceProvider(_ *http.Request, _ string) (*saml.EntityDescriptor, error) {
	resp, err := p.client.Get(p.metadataURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	metadata := &saml.EntityDescriptor{}
	if err := xml.Unmarshal(raw, metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test IdP"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	tidp := &testIDP{nameID: "00u-default"}
	mux := http.NewServeMux()
	tidp.server = httptest.NewServer(mux)
	t.Cleanup(tidp.server.Close)

	base, _ := url.Parse(tidp.server.URL)
	tidp.idp = &saml.IdentityProvider{
		Key:             key,
		Certificate:     cert,
		MetadataURL:     *base.JoinPath("/metadata"),
		SSOURL:          *base.JoinPath("/sso"),
		SessionProvider: staticSessionProvider{tidp},
		AssertionMaker:  emailAttributeMaker{},
	}
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		tidp.idp.ServeMetadata(w, r)
	})
	mux.HandleFunc("/sso", func(w http.ResponseWriter, r *http.Request) {
		tidp.idp.ServeSSO(w, r)
	})
	// An unsolicited response: no AuthnRequest, hence no InResponseTo.
	mux.HandleFunc("/idp-initiated", func(w http.ResponseWriter, r *http.Request) {
		tidp.idp.ServeIDPInitiated(w, r, tidp.spEntityID, "")
	})
	return tidp
}

// --- SP-side environment ---

type testLogWriter struct{ t *testing.T }

// syncBuffer captures logs written from handler goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// toggleNonces wraps the real store so a test can simulate Redis going
// away mid-flow without touching the session store.
type toggleNonces struct {
	real *redisstore.NonceStore
	fail atomic.Bool
}

var errNonceDown = errors.New("nonce store down")

func (n *toggleNonces) PutRequestID(ctx context.Context, id string, ttl time.Duration) error {
	if n.fail.Load() {
		return errNonceDown
	}
	return n.real.PutRequestID(ctx, id, ttl)
}

func (n *toggleNonces) ConsumeRequestID(ctx context.Context, id string) (bool, error) {
	if n.fail.Load() {
		return false, errNonceDown
	}
	return n.real.ConsumeRequestID(ctx, id)
}

func (n *toggleNonces) MarkAssertionUsed(ctx context.Context, id string, ttl time.Duration) (bool, error) {
	if n.fail.Load() {
		return false, errNonceDown
	}
	return n.real.MarkAssertionUsed(ctx, id, ttl)
}

type spEnv struct {
	server  *httptest.Server
	store   *memStore
	svc     *Service
	client  *http.Client
	redis   *redis.Client
	nonces  *toggleNonces
	logs    *syncBuffer
	success atomic.Int64
	failure atomic.Int64
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6380", DB: 14})
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Skipf("Redis from docker-compose is not available: %v (run `mise run up`)", err)
	}
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func newSPEnv(t *testing.T, idp *testIDP) *spEnv {
	return newSPEnvWith(t, idp, func(*Config) {})
}

func newSPEnvWith(t *testing.T, idp *testIDP, tweak func(*Config)) *spEnv {
	t.Helper()
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(io.MultiWriter(&testLogWriter{t}, logs), nil))
	store := newMemStore()
	rdb := testRedis(t)

	sessions := session.NewManager(rdb, session.Config{
		CookieName:    "__identity_session_test",
		IdleTimeout:   time.Minute,
		Lifetime:      time.Hour,
		MaxConcurrent: 100,
	}, logger)
	nonces := &toggleNonces{real: redisstore.NewNonceStore(rdb)}

	mux := http.NewServeMux()
	server := httptest.NewServer(sessions.Middleware(mux))
	t.Cleanup(server.Close)

	cfg := Config{
		BaseURL:          server.URL,
		FrontendBaseURL:  "http://frontend.example.com",
		IDPMetadataURL:   idp.server.URL + "/metadata",
		RelayStateSecret: "test-secret",
	}
	tweak(&cfg)
	svc, err := New(t.Context(), cfg, store, sessions, nonces, logger)
	if err != nil {
		t.Fatalf("samlsso.New: %v", err)
	}
	env := &spEnv{store: store, redis: rdb, nonces: nonces, logs: logs}
	svc.SetMetrics(countingSignIns{env})
	svc.Register(mux)

	session.NewHandlers(sessions, spUserSource{store}, nil).Register(mux)

	// The IdP resolves our SP by fetching its metadata endpoint.
	idp.spEntityID = server.URL + "/auth/saml/metadata"
	idp.idp.ServiceProviderProvider = staticSPProvider{
		metadataURL: idp.spEntityID,
		client:      http.DefaultClient,
	}

	env.server = server
	env.svc = svc
	env.client = freshClient()
	return env
}

// freshClient is a separate browser: its own cookie jar.
func freshClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

type countingSignIns struct{ e *spEnv }

func (c countingSignIns) Success() { c.e.success.Add(1) }
func (c countingSignIns) Failure() { c.e.failure.Add(1) }

type spUserSource struct{ store *memStore }

func (s spUserSource) FindByID(ctx context.Context, id string) (*identity.User, error) {
	return s.store.FindByID(ctx, id)
}

func (s spUserSource) Permissions(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (s spUserSource) IsActive(ctx context.Context, id string) (bool, error) {
	u, err := s.store.FindByID(ctx, id)
	if errors.Is(err, identity.ErrUserNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return u.Active, nil
}

var (
	samlResponseRe = regexp.MustCompile(`name="SAMLResponse" value="([^"]+)"`)
	relayStateRe   = regexp.MustCompile(`name="RelayState" value="([^"]+)"`)
)

func extractResponse(t *testing.T, page []byte) (string, string) {
	t.Helper()
	respMatch := samlResponseRe.FindSubmatch(page)
	if respMatch == nil {
		t.Fatalf("IdP page has no SAMLResponse form field: %s", page[:min(len(page), 400)])
	}
	relay := ""
	if relayMatch := relayStateRe.FindSubmatch(page); relayMatch != nil {
		relay = html.UnescapeString(string(relayMatch[1]))
	}
	return html.UnescapeString(string(respMatch[1])), relay
}

// driveSSO performs init -> IdP -> returns (samlResponse, relayState).
func (e *spEnv) driveSSO(t *testing.T, next string) (string, string) {
	t.Helper()
	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	initURL := e.server.URL + "/auth/saml/init"
	if next != "" {
		initURL += "?next=" + url.QueryEscape(next)
	}
	resp, err := noRedirect.Get(initURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("init = %d, want 302", resp.StatusCode)
	}
	idpURL := resp.Header.Get("Location")
	if !strings.Contains(idpURL, "/sso") || !strings.Contains(idpURL, "SAMLRequest=") {
		t.Fatalf("init redirect = %q, want IdP SSO URL with SAMLRequest", idpURL)
	}

	idpResp, err := http.Get(idpURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idpResp.Body.Close() }()
	page, err := io.ReadAll(idpResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return extractResponse(t, page)
}

// driveIDPInitiated fetches an unsolicited response straight from the IdP.
func (e *spEnv) driveIDPInitiated(t *testing.T, idp *testIDP) string {
	t.Helper()
	resp, err := http.Get(idp.server.URL + "/idp-initiated")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	samlResp, _ := extractResponse(t, page)
	return samlResp
}

func (e *spEnv) postACS(t *testing.T, samlResponse, relayState string) *http.Response {
	t.Helper()
	return e.postACSWith(t, e.client, samlResponse, relayState)
}

func (e *spEnv) postACSWith(t *testing.T, client *http.Client, samlResponse, relayState string) *http.Response {
	t.Helper()
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	form := url.Values{"SAMLResponse": {samlResponse}, "RelayState": {relayState}}
	resp, err := noRedirect.PostForm(e.server.URL+"/auth/saml/acs", form)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *spEnv) me(t *testing.T) *http.Response {
	t.Helper()
	return e.meWith(t, e.client)
}

func (e *spEnv) meWith(t *testing.T, client *http.Client) *http.Response {
	t.Helper()
	resp, err := client.Get(e.server.URL + "/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// --- tests ---

func TestNewFailsOnInvalidMetadata(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not XML metadata"))
	}))
	defer bad.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := New(t.Context(), Config{
		BaseURL:          "http://sp.example.com",
		FrontendBaseURL:  "http://front.example.com",
		IDPMetadataURL:   bad.URL,
		RelayStateSecret: "s",
	}, newMemStore(), nil, nil, logger)
	if err == nil {
		t.Fatal("New with invalid IdP metadata must fail")
	}
}

func TestSPMetadataEndpoint(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)

	resp, err := http.Get(e.server.URL + "/auth/saml/metadata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Content-Type"); got != "application/samlmetadata+xml" {
		t.Errorf("content type = %q", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "/auth/saml/acs") ||
		!strings.Contains(string(body), "/auth/saml/metadata") {
		t.Errorf("metadata must contain ACS URL and entity ID, got: %.300s", body)
	}
}

func TestFullSignInFlow(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID, idp.email = "00u-dev-1", "dev@example.com"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp, relay := e.driveSSO(t, "/orders/42?tab=docs")
	resp := e.postACS(t, samlResp, relay)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("ACS = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "http://frontend.example.com/orders/42?tab=docs" {
		t.Errorf("redirect = %q, want frontend + next path", loc)
	}
	if len(e.store.identities) != 1 {
		t.Errorf("sign-in must not create identities, got %v", e.store.identities)
	}
	if e.store.lastSignIn["u1"] != 1 {
		t.Errorf("last sign-in touches = %d, want 1", e.store.lastSignIn["u1"])
	}

	// Cookie from ACS response is in the jar: /auth/me works.
	if me := e.me(t); me.StatusCode != http.StatusOK {
		t.Errorf("me after login = %d, want 200", me.StatusCode)
	}
	if e.success.Load() != 1 || e.failure.Load() != 0 {
		t.Errorf("sign-in counters = %d success / %d failure, want 1/0", e.success.Load(), e.failure.Load())
	}
	if strings.Contains(e.logs.String(), "differs from directory") {
		t.Error("matching email must not be reported as drift")
	}
}

func TestTamperedResponseRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp, relay := e.driveSSO(t, "")
	// Corrupt one character inside the base64 payload.
	tampered := []byte(samlResp)
	mid := len(tampered) / 2
	if tampered[mid] != 'A' {
		tampered[mid] = 'A'
	} else {
		tampered[mid] = 'B'
	}

	resp := e.postACS(t, string(tampered), relay)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered ACS = %d, want 401", resp.StatusCode)
	}
	if e.failure.Load() != 1 || e.success.Load() != 0 {
		t.Errorf("sign-in counters = %d success / %d failure, want 0/1", e.success.Load(), e.failure.Load())
	}
	if me := e.me(t); me.StatusCode != http.StatusUnauthorized {
		t.Error("no session must exist after rejected response")
	}
}

func TestUnknownSubjectRejectedEvenWhenEmailMatches(t *testing.T) {
	// The directory has an active user with exactly this email, but the
	// subject was never provisioned. Linking by email here is the retired
	// fallback: a matching address must not produce a session or a link.
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID, idp.email = "00u-unprovisioned", "dev@example.com"
	e.store.addUser("u1", "dev@example.com", true)

	samlResp, relay := e.driveSSO(t, "")
	resp := e.postACS(t, samlResp, relay)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unprovisioned subject ACS = %d, want 401", resp.StatusCode)
	}
	if len(e.store.identities) != 0 {
		t.Errorf("no identity must be attached at sign-in, got %v", e.store.identities)
	}
	if me := e.me(t); me.StatusCode != http.StatusUnauthorized {
		t.Error("no session must exist")
	}
	if e.failure.Load() != 1 {
		t.Errorf("failure counter = %d, want 1", e.failure.Load())
	}
}

func TestDeactivatedUserRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-off"
	e.store.addUser("u2", "off@example.com", false)
	e.store.link("u2", "00u-off")

	samlResp, relay := e.driveSSO(t, "")
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deactivated user ACS = %d, want 401", resp.StatusCode)
	}
	if _, linked := e.store.identities[identity.ProviderOkta+"|00u-off"]; !linked {
		t.Error("existing link must survive a rejected sign-in")
	}
}

func TestEmailChangeAtIdPKeepsSignIn(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID, idp.email = "00u-dev-1", "dev@example.com"
	u := e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp, relay := e.driveSSO(t, "")
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusFound {
		t.Fatalf("first login = %d", resp.StatusCode)
	}

	// The person is renamed at the IdP; SCIM has already pushed the new
	// address to the directory. The subject is unchanged.
	idp.email = "renamed@example.com"
	delete(e.store.byEmail, "dev@example.com")
	u.Email = "renamed@example.com"
	e.store.byEmail["renamed@example.com"] = "u1"

	samlResp2, relay2 := e.postSecondLogin(t, freshClient())
	_ = samlResp2
	_ = relay2
	if e.store.lastSignIn["u1"] != 2 {
		t.Errorf("sign-ins = %d, want 2 (resolved via stable subject)", e.store.lastSignIn["u1"])
	}
	if u.Email != "renamed@example.com" {
		t.Errorf("directory email = %q, sign-in must not write it", u.Email)
	}
	if strings.Contains(e.logs.String(), "differs from directory") {
		t.Error("in-sync email must not be reported as drift")
	}

	// Now the IdP is ahead of provisioning: sign-in still works, the
	// directory keeps its value, and the drift is visible in the logs.
	idp.email = "ahead@example.com"
	e.postSecondLogin(t, freshClient())
	if u.Email != "renamed@example.com" {
		t.Errorf("directory email = %q, assertion must not overwrite it", u.Email)
	}
	if !strings.Contains(e.logs.String(), "differs from directory") {
		t.Error("email drift between IdP and directory must be logged")
	}
	if strings.Contains(e.logs.String(), "ahead@example.com") || strings.Contains(e.logs.String(), "renamed@example.com") {
		t.Error("drift log must not contain email addresses")
	}
}

// postSecondLogin drives a fresh sign-in in a separate browser and requires
// it to succeed.
func (e *spEnv) postSecondLogin(t *testing.T, client *http.Client) (string, string) {
	t.Helper()
	samlResp, relay := e.driveSSO(t, "")
	if resp := e.postACSWith(t, client, samlResp, relay); resp.StatusCode != http.StatusFound {
		t.Fatalf("login = %d, want 302", resp.StatusCode)
	}
	return samlResp, relay
}

func TestResponseReplayRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp, relay := e.driveSSO(t, "")
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusFound {
		t.Fatalf("first delivery = %d, want 302", resp.StatusCode)
	}

	// The same signed response, replayed from another browser well within
	// the assertion's validity window.
	other := freshClient()
	if resp := e.postACSWith(t, other, samlResp, relay); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed delivery = %d, want 401", resp.StatusCode)
	}
	if me := e.meWith(t, other); me.StatusCode != http.StatusUnauthorized {
		t.Error("replay must not yield a session")
	}
	if e.success.Load() != 1 || e.failure.Load() != 1 {
		t.Errorf("sign-in counters = %d/%d, want 1 success / 1 failure", e.success.Load(), e.failure.Load())
	}
}

func TestConcurrentDeliveryAcceptsOnce(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp, relay := e.driveSSO(t, "")

	const deliveries = 8
	codes := make([]int, deliveries)
	var wg sync.WaitGroup
	for i := range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := freshClient()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			resp, err := client.PostForm(e.server.URL+"/auth/saml/acs",
				url.Values{"SAMLResponse": {samlResp}, "RelayState": {relay}})
			if err != nil {
				t.Error(err)
				return
			}
			_ = resp.Body.Close()
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()

	accepted := 0
	for _, code := range codes {
		switch code {
		case http.StatusFound:
			accepted++
		case http.StatusUnauthorized:
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	if accepted != 1 {
		t.Errorf("accepted deliveries = %d, want exactly 1 (codes %v)", accepted, codes)
	}
}

func TestUnknownRequestIDRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp, relay := e.driveSSO(t, "")
	// The request id expired (or was issued by nobody): a valid signature
	// alone does not sign anyone in.
	if err := e.redis.FlushDB(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ACS without a known request id = %d, want 401", resp.StatusCode)
	}
	if me := e.me(t); me.StatusCode != http.StatusUnauthorized {
		t.Error("no session must exist")
	}
}

func TestIdPInitiatedRejectedByDefault(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp := e.driveIDPInitiated(t, idp)
	if resp := e.postACS(t, samlResp, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsolicited response = %d, want 401 while IdP-initiated is off", resp.StatusCode)
	}
	if me := e.me(t); me.StatusCode != http.StatusUnauthorized {
		t.Error("no session must exist")
	}
}

func TestIdPInitiatedAllowedByFlagStillOneShot(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnvWith(t, idp, func(c *Config) { c.AllowIDPInitiated = true })
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	samlResp := e.driveIDPInitiated(t, idp)
	if resp := e.postACS(t, samlResp, ""); resp.StatusCode != http.StatusFound {
		t.Fatalf("unsolicited response with flag = %d, want 302", resp.StatusCode)
	}
	if me := e.me(t); me.StatusCode != http.StatusOK {
		t.Error("session must exist after accepted IdP-initiated sign-in")
	}
	// No request id to consume on this path, so the assertion cache is the
	// only thing standing between a replay and a second session.
	other := freshClient()
	if resp := e.postACSWith(t, other, samlResp, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed IdP-initiated response = %d, want 401", resp.StatusCode)
	}
}

func TestNonceStoreDownFailsClosed(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "00u-dev-1"
	e.store.addUser("u1", "dev@example.com", true)
	e.store.link("u1", "00u-dev-1")

	// Init cannot record the request id: no redirect to the IdP.
	e.nonces.fail.Store(true)
	resp, err := http.Get(e.server.URL + "/auth/saml/init")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("init with nonce store down = %d, want 503", resp.StatusCode)
	}

	// A response that was issued while Redis was healthy arrives after
	// Redis went away: it cannot be told from a replay, so it is not
	// accepted — and not rejected as invalid either.
	e.nonces.fail.Store(false)
	samlResp, relay := e.driveSSO(t, "")
	e.nonces.fail.Store(true)
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ACS with nonce store down = %d, want 503", resp.StatusCode)
	}
	if me := e.me(t); me.StatusCode != http.StatusUnauthorized {
		t.Error("no session must exist")
	}
	if e.success.Load() != 0 {
		t.Errorf("success counter = %d, want 0", e.success.Load())
	}
}
