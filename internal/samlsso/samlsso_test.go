package samlsso

import (
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
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/redis/go-redis/v9"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
	"github.com/xometry-europe-gmbh/identity-service/internal/session"
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

func (m *memStore) HasIdentity(_ context.Context, userID, provider string) (bool, error) {
	for key, id := range m.identities {
		if id == userID && strings.HasPrefix(key, provider+"|") {
			return true, nil
		}
	}
	return false, nil
}

func (m *memStore) AttachIdentity(_ context.Context, userID, provider, subject string) error {
	m.identities[provider+"|"+subject] = userID
	return nil
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
	// nameID returned for every authenticated session
	nameID string
}

type staticSessionProvider struct{ t *testIDP }

func (s staticSessionProvider) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	return &saml.Session{
		ID:         "session-1",
		CreateTime: saml.TimeNow(),
		ExpireTime: saml.TimeNow().Add(time.Hour),
		Index:      "idx-1",
		NameID:     s.t.nameID,
	}
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

	tidp := &testIDP{}
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
	}
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		tidp.idp.ServeMetadata(w, r)
	})
	mux.HandleFunc("/sso", func(w http.ResponseWriter, r *http.Request) {
		tidp.idp.ServeSSO(w, r)
	})
	return tidp
}

// --- SP-side environment ---

type testLogWriter struct{ t *testing.T }

func (w *testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

type spEnv struct {
	server *httptest.Server
	store  *memStore
	svc    *Service
	client *http.Client
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
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&testLogWriter{t}, nil))
	store := newMemStore()

	sessions := session.NewManager(testRedis(t), session.Config{
		CookieName:    "__identity_session_test",
		IdleTimeout:   time.Minute,
		Lifetime:      time.Hour,
		MaxConcurrent: 100,
	}, logger)

	mux := http.NewServeMux()
	server := httptest.NewServer(sessions.Middleware(mux))
	t.Cleanup(server.Close)

	svc, err := New(t.Context(), Config{
		BaseURL:          server.URL,
		FrontendBaseURL:  "http://frontend.example.com",
		IDPMetadataURL:   idp.server.URL + "/metadata",
		RelayStateSecret: "test-secret",
	}, store, sessions, logger)
	if err != nil {
		t.Fatalf("samlsso.New: %v", err)
	}
	svc.Register(mux)

	session.NewHandlers(sessions, spUserSource{store}, nil).Register(mux)

	// The IdP resolves our SP by fetching its metadata endpoint.
	idp.idp.ServiceProviderProvider = staticSPProvider{
		metadataURL: server.URL + "/auth/saml/metadata",
		client:      http.DefaultClient,
	}

	jar, _ := cookiejar.New(nil)
	return &spEnv{server: server, store: store, svc: svc, client: &http.Client{Jar: jar}}
}

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
	respMatch := samlResponseRe.FindSubmatch(page)
	if respMatch == nil {
		t.Fatalf("IdP page has no SAMLResponse form field: %s", page[:min(len(page), 400)])
	}
	relayMatch := relayStateRe.FindSubmatch(page)
	relay := ""
	if relayMatch != nil {
		relay = html.UnescapeString(string(relayMatch[1]))
	}
	return html.UnescapeString(string(respMatch[1])), relay
}

func (e *spEnv) postACS(t *testing.T, samlResponse, relayState string) *http.Response {
	t.Helper()
	noRedirect := *e.client
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
	resp, err := e.client.Get(e.server.URL + "/auth/me")
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
	}, newMemStore(), nil, logger)
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
	idp.nameID = "dev@example.com"
	e.store.addUser("u1", "dev@example.com", true)

	samlResp, relay := e.driveSSO(t, "/orders/42?tab=docs")
	resp := e.postACS(t, samlResp, relay)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("ACS = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "http://frontend.example.com/orders/42?tab=docs" {
		t.Errorf("redirect = %q, want frontend + next path", loc)
	}
	if e.store.identities[identity.ProviderOkta+"|dev@example.com"] != "u1" {
		t.Error("identity mapping must be attached on first login")
	}
	if e.store.lastSignIn["u1"] != 1 {
		t.Errorf("last sign-in touches = %d, want 1", e.store.lastSignIn["u1"])
	}

	// Cookie from ACS response is in the jar: /auth/me works.
	if me := e.me(t); me.StatusCode != http.StatusOK {
		t.Errorf("me after login = %d, want 200", me.StatusCode)
	}
}

func TestTamperedResponseRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "dev@example.com"
	e.store.addUser("u1", "dev@example.com", true)

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
	if me := e.me(t); me.StatusCode != http.StatusUnauthorized {
		t.Error("no session must exist after rejected response")
	}
}

func TestUnknownUserRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "ghost@example.com"

	samlResp, relay := e.driveSSO(t, "")
	resp := e.postACS(t, samlResp, relay)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown user ACS = %d, want 401", resp.StatusCode)
	}
	if len(e.store.identities) != 0 {
		t.Error("no identity must be attached for unknown user")
	}
}

func TestDeactivatedUserRejected(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "off@example.com"
	e.store.addUser("u2", "off@example.com", false)

	samlResp, relay := e.driveSSO(t, "")
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deactivated user ACS = %d, want 401", resp.StatusCode)
	}
}

func TestReloginBySubjectAfterEmailChange(t *testing.T) {
	idp := newTestIDP(t)
	e := newSPEnv(t, idp)
	idp.nameID = "dev@example.com"
	u := e.store.addUser("u1", "dev@example.com", true)

	samlResp, relay := e.driveSSO(t, "")
	if resp := e.postACS(t, samlResp, relay); resp.StatusCode != http.StatusFound {
		t.Fatalf("first login = %d", resp.StatusCode)
	}

	// The user's email changes locally; the subject stays the same.
	delete(e.store.byEmail, "dev@example.com")
	u.Email = "renamed@example.com"
	e.store.byEmail["renamed@example.com"] = "u1"

	samlResp2, relay2 := e.driveSSO(t, "")
	if resp := e.postACS(t, samlResp2, relay2); resp.StatusCode != http.StatusFound {
		t.Fatalf("re-login by stored subject = %d, want 302", resp.StatusCode)
	}
	if e.store.lastSignIn["u1"] != 2 {
		t.Errorf("sign-ins = %d, want 2 (resolved via stored subject)", e.store.lastSignIn["u1"])
	}
}
