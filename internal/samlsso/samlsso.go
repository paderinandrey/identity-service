// Package samlsso implements the SAML 2.0 service-provider flow against
// Okta: sign-in initiation, ACS callback processing and SP metadata.
//
// The subject of a sign-in is the IdP's immutable user id carried by a
// persistent NameID — the same value provisioning stores as the Okta
// identity — so an email change never breaks the mapping. Every
// AuthnRequest id and every accepted assertion id is one-shot in a store
// shared by all replicas, which is what makes a replayed response fail.
package samlsso

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"github.com/paderinandrey/identity-service/internal/identity"
	"github.com/paderinandrey/identity-service/internal/session"
)

const (
	// maxResponseBytes bounds the ACS request body (mirrors GSH).
	maxResponseBytes = 200_000
	// clockSkew narrows crewjam's default 180s to GSH's 60s.
	clockSkew = 60 * time.Second
	// metadataRefreshInterval re-fetches IdP metadata (Okta cert rotation).
	metadataRefreshInterval = 24 * time.Hour
	// requestTTL bounds how long an AuthnRequest may wait for its response.
	requestTTL = 10 * time.Minute
	// assertionTTLFallback covers assertions without NotOnOrAfter.
	assertionTTLFallback = 10 * time.Minute
	// emailAttribute is the assertion attribute compared with the directory.
	emailAttribute = "email"
)

// Config carries everything the SAML flow needs.
type Config struct {
	BaseURL            string // e.g. https://id.example.com
	FrontendBaseURL    string // where the browser lands after login
	IDPMetadataURL     string
	RelayStateSecret   string
	MetadataHTTPClient *http.Client // optional, defaults to a 10s-timeout client
	// AllowIDPInitiated accepts responses without InResponseTo. Off by
	// default; assertion one-shot protection applies either way.
	AllowIDPInitiated bool
}

// NonceStore keeps the one-shot identifiers of the sign-in flow. It must be
// shared by every replica: a response may land on any of them.
type NonceStore interface {
	// PutRequestID remembers an issued AuthnRequest id for ttl.
	PutRequestID(ctx context.Context, id string, ttl time.Duration) error
	// ConsumeRequestID atomically forgets the id and reports whether it
	// was known, so concurrent deliveries agree on a single winner.
	ConsumeRequestID(ctx context.Context, id string) (bool, error)
	// MarkAssertionUsed records the assertion id for ttl and reports
	// whether this was the first time it was seen.
	MarkAssertionUsed(ctx context.Context, id string, ttl time.Duration) (bool, error)
}

// SignInMetrics counts sign-in outcomes; nil disables instrumentation.
type SignInMetrics interface {
	Success()
	Failure()
}

// Service owns the SP state and HTTP handlers of the SAML flow.
type Service struct {
	acsURL            url.URL
	metadataURL       url.URL
	frontendURL       url.URL
	idpMetadata       atomic.Pointer[saml.EntityDescriptor]
	metadataSrc       url.URL
	httpClient        *http.Client
	relay             *RelayState
	store             identity.Store
	sessions          *session.Manager
	nonces            NonceStore
	allowIDPInitiated bool
	logger            *slog.Logger
	metrics           SignInMetrics
}

// New fetches IdP metadata (fail-fast) and builds the service.
func New(ctx context.Context, cfg Config, store identity.Store, sessions *session.Manager, nonces NonceStore, logger *slog.Logger) (*Service, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("samlsso: invalid base URL %q", cfg.BaseURL)
	}
	frontend, err := url.Parse(cfg.FrontendBaseURL)
	if err != nil || frontend.Scheme == "" || frontend.Host == "" {
		return nil, fmt.Errorf("samlsso: invalid frontend base URL %q", cfg.FrontendBaseURL)
	}
	metadataSrc, err := url.Parse(cfg.IDPMetadataURL)
	if err != nil || metadataSrc.Scheme == "" {
		return nil, fmt.Errorf("samlsso: invalid IdP metadata URL %q", cfg.IDPMetadataURL)
	}

	httpClient := cfg.MetadataHTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	s := &Service{
		acsURL:            *base.JoinPath("/auth/saml/acs"),
		metadataURL:       *base.JoinPath("/auth/saml/metadata"),
		frontendURL:       *frontend,
		metadataSrc:       *metadataSrc,
		httpClient:        httpClient,
		relay:             NewRelayState(cfg.RelayStateSecret),
		store:             store,
		sessions:          sessions,
		nonces:            nonces,
		allowIDPInitiated: cfg.AllowIDPInitiated,
		logger:            logger,
	}

	idp, err := samlsp.FetchMetadata(ctx, httpClient, *metadataSrc)
	if err != nil {
		return nil, fmt.Errorf("samlsso: fetch IdP metadata: %w", err)
	}
	s.idpMetadata.Store(idp)

	// Narrow crewjam's default 180s clock skew to GSH's 60s. Package-level
	// by library design; the value is constant across the process.
	saml.MaxClockSkew = clockSkew

	return s, nil
}

// SetMetrics attaches sign-in instrumentation.
func (s *Service) SetMetrics(m SignInMetrics) { s.metrics = m }

func (s *Service) countSignIn(success bool) {
	if s.metrics == nil {
		return
	}
	if success {
		s.metrics.Success()
	} else {
		s.metrics.Failure()
	}
}

// RefreshMetadataLoop re-fetches IdP metadata until ctx is done. Failures
// keep the last valid metadata and only log a warning.
func (s *Service) RefreshMetadataLoop(ctx context.Context) {
	ticker := time.NewTicker(metadataRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idp, err := samlsp.FetchMetadata(ctx, s.httpClient, s.metadataSrc)
			if err != nil {
				s.logger.Warn("IdP metadata refresh failed, keeping previous", "error", err)
				continue
			}
			s.idpMetadata.Store(idp)
			s.logger.Info("IdP metadata refreshed")
		}
	}
}

// sp builds a per-request ServiceProvider with the current IdP metadata.
// idpInitiated relaxes the InResponseTo checks for a response that carries
// none; SP-initiated responses always get the full validation.
func (s *Service) sp(idpInitiated bool) saml.ServiceProvider {
	return saml.ServiceProvider{
		EntityID:    s.metadataURL.String(),
		MetadataURL: s.metadataURL,
		AcsURL:      s.acsURL,
		IDPMetadata: s.idpMetadata.Load(),
		// Persistent NameID carries the IdP's immutable user id (Okta
		// user.id, Keycloak user UUID) — the subject must survive email
		// changes, and it must equal the SCIM externalId.
		AuthnNameIDFormat: saml.PersistentNameIDFormat,
		AllowIDPInitiated: idpInitiated,
	}
}

// Register mounts the SAML routes. They must NOT require a session.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/saml/init", s.handleInit)
	mux.HandleFunc("POST /auth/saml/acs", s.handleACS)
	mux.HandleFunc("GET /auth/saml/metadata", s.handleMetadata)
}

func (s *Service) handleInit(w http.ResponseWriter, r *http.Request) {
	sp := s.sp(false)
	req, err := sp.MakeAuthenticationRequest(
		sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		s.logger.Error("failed to build AuthnRequest", "error", err)
		http.Error(w, "SSO unavailable", http.StatusServiceUnavailable)
		return
	}
	// The id is what ties the response back to this sign-in; without it
	// recorded there is nothing to consume at ACS, so fail closed.
	if err := s.nonces.PutRequestID(r.Context(), req.ID, requestTTL); err != nil {
		s.logger.Error("failed to record AuthnRequest id", "error", err)
		http.Error(w, "SSO unavailable", http.StatusServiceUnavailable)
		return
	}
	redirect, err := req.Redirect(s.relay.Encode(r.URL.Query().Get("next")), &sp)
	if err != nil {
		s.logger.Error("failed to build redirect URL", "error", err)
		http.Error(w, "SSO unavailable", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Service) handleACS(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxResponseBytes)
	// ParseResponse reads the pre-parsed form; oversized bodies fail here.
	if err := r.ParseForm(); err != nil {
		s.reject(w, "form parse failed or body too large")
		return
	}

	inResponseTo, err := peekInResponseTo(r.PostFormValue("SAMLResponse"))
	if err != nil {
		s.reject(w, "malformed response envelope")
		return
	}
	idpInitiated := inResponseTo == ""
	var possibleRequestIDs []string
	if idpInitiated {
		if !s.allowIDPInitiated {
			s.reject(w, "idp-initiated response not allowed")
			return
		}
	} else {
		// Consume before parsing: if two replicas receive the same
		// response, only one owns the request id. A response that then
		// fails validation has burnt its id, and the user starts over —
		// safer than a window between parsing and consuming.
		known, err := s.nonces.ConsumeRequestID(r.Context(), inResponseTo)
		if err != nil {
			s.unavailable(w, "failed to consume AuthnRequest id", err)
			return
		}
		if !known {
			s.reject(w, "unknown or already used request id")
			return
		}
		possibleRequestIDs = []string{inResponseTo}
	}

	sp := s.sp(idpInitiated)
	assertion, err := sp.ParseResponse(r, possibleRequestIDs)
	if err != nil {
		s.logSAMLError(err)
		s.countSignIn(false)
		http.Error(w, "SAML validation failed", http.StatusUnauthorized)
		return
	}
	if assertion.ID == "" {
		s.reject(w, "assertion without ID")
		return
	}
	first, err := s.nonces.MarkAssertionUsed(r.Context(), assertion.ID, assertionTTL(assertion))
	if err != nil {
		s.unavailable(w, "failed to record assertion id", err)
		return
	}
	if !first {
		s.reject(w, "assertion replayed")
		return
	}

	subject := ""
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		subject = assertion.Subject.NameID.Value
	}
	if subject == "" {
		s.reject(w, "assertion without NameID")
		return
	}

	user, err := identity.Resolve(r.Context(), s.store, identity.ProviderOkta, subject)
	if errors.Is(err, identity.ErrUserNotFound) {
		s.countSignIn(false)
		http.Error(w, "user not found", http.StatusUnauthorized)
		return
	}
	if err != nil {
		s.logger.Error("identity resolution failed", "error", err)
		http.Error(w, "sign-in failed", http.StatusServiceUnavailable)
		return
	}
	// Provisioning owns the email; the assertion never writes it. A drift
	// means the SCIM sync is behind, which is worth seeing in the logs.
	if e := assertionEmail(assertion); e != "" && identity.NormalizeEmail(e) != user.Email {
		s.logger.Warn("email in assertion differs from directory; provisioning may be behind", "user_id", user.ID)
	}

	if err := s.sessions.Start(r.Context(), user.ID); err != nil {
		s.logger.Error("failed to start session", "error", err)
		http.Error(w, "sign-in failed", http.StatusServiceUnavailable)
		return
	}
	if err := s.store.TouchLastSignIn(r.Context(), user.ID); err != nil {
		s.logger.Warn("failed to record last sign-in", "error", err)
	}
	s.logger.Info("user signed in", "user_id", user.ID)
	s.countSignIn(true)

	http.Redirect(w, r, s.frontendRedirectURL(s.relay.Decode(r.PostFormValue("RelayState"))), http.StatusFound)
}

// reject answers 401 for a response that must not sign anyone in.
func (s *Service) reject(w http.ResponseWriter, reason string) {
	s.logger.Warn("SAML response rejected", "reason", reason)
	s.countSignIn(false)
	http.Error(w, "SAML validation failed", http.StatusUnauthorized)
}

// unavailable answers 503 when the one-shot store cannot be consulted:
// without it a replay cannot be told from a first delivery.
func (s *Service) unavailable(w http.ResponseWriter, msg string, err error) {
	s.logger.Error(msg, "error", err)
	http.Error(w, "sign-in unavailable", http.StatusServiceUnavailable)
}

func (s *Service) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	sp := s.sp(false)
	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		http.Error(w, "metadata unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(out)
}

// peekInResponseTo reads the InResponseTo attribute of the Response root
// element before signature validation. The value is only used as a lookup
// key: crewjam still verifies the signature and re-checks InResponseTo, so
// a forged value can at most burn a request id the attacker already knows.
func peekInResponseTo(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	var root struct {
		InResponseTo string `xml:"InResponseTo,attr"`
	}
	if err := xml.Unmarshal(raw, &root); err != nil {
		return "", err
	}
	return root.InResponseTo, nil
}

// assertionTTL keeps the assertion id at least as long as the assertion
// itself could be accepted, clock skew included.
func assertionTTL(a *saml.Assertion) time.Duration {
	if a.Conditions == nil || a.Conditions.NotOnOrAfter.IsZero() {
		return assertionTTLFallback + clockSkew
	}
	return time.Until(a.Conditions.NotOnOrAfter) + clockSkew
}

// assertionEmail returns the email attribute of the assertion, if any.
func assertionEmail(a *saml.Assertion) string {
	for _, statement := range a.AttributeStatements {
		for _, attr := range statement.Attributes {
			if !strings.EqualFold(attr.Name, emailAttribute) && !strings.EqualFold(attr.FriendlyName, emailAttribute) {
				continue
			}
			for _, v := range attr.Values {
				if v.Value != "" {
					return v.Value
				}
			}
		}
	}
	return ""
}

// frontendRedirectURL grafts the safe relative path onto the frontend base.
func (s *Service) frontendRedirectURL(path string) string {
	target := s.frontendURL
	rel, err := url.Parse(SanitizePath(path))
	if err != nil {
		rel = &url.URL{Path: "/"}
	}
	target.Path = rel.Path
	target.RawQuery = rel.RawQuery
	target.Fragment = rel.Fragment
	return target.String()
}

// logSAMLError records the failure category without assertion contents.
func (s *Service) logSAMLError(err error) {
	var invalid *saml.InvalidResponseError
	if errors.As(err, &invalid) {
		// PrivateErr describes the validation failure; the raw response
		// XML held by the error is deliberately not logged.
		s.logger.Warn("SAML response rejected", "reason", invalid.PrivateErr.Error())
		return
	}
	s.logger.Warn("SAML response rejected", "reason", err.Error())
}
