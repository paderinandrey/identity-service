// Package samlsso implements the SAML 2.0 service-provider flow against
// Okta: sign-in initiation, ACS callback processing and SP metadata.
// Behavior mirrors the proven GSH implementation.
package samlsso

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"github.com/xometry-europe-gmbh/identity-service/internal/identity"
	"github.com/xometry-europe-gmbh/identity-service/internal/session"
)

const (
	// maxResponseBytes bounds the ACS request body (mirrors GSH).
	maxResponseBytes = 200_000
	// clockSkew narrows crewjam's default 180s to GSH's 60s.
	clockSkew = 60 * time.Second
	// metadataRefreshInterval re-fetches IdP metadata (Okta cert rotation).
	metadataRefreshInterval = 24 * time.Hour
)

// Config carries everything the SAML flow needs.
type Config struct {
	BaseURL            string // e.g. https://id.example.com
	FrontendBaseURL    string // where the browser lands after login
	IDPMetadataURL     string
	RelayStateSecret   string
	MetadataHTTPClient *http.Client // optional, defaults to a 10s-timeout client
}

// SignInMetrics counts sign-in outcomes; nil disables instrumentation.
type SignInMetrics interface {
	Success()
	Failure()
}

// Service owns the SP state and HTTP handlers of the SAML flow.
type Service struct {
	acsURL      url.URL
	metadataURL url.URL
	frontendURL url.URL
	idpMetadata atomic.Pointer[saml.EntityDescriptor]
	metadataSrc url.URL
	httpClient  *http.Client
	relay       *RelayState
	store       identity.Store
	sessions    *session.Manager
	logger      *slog.Logger
	metrics     SignInMetrics
}

// New fetches IdP metadata (fail-fast) and builds the service.
func New(ctx context.Context, cfg Config, store identity.Store, sessions *session.Manager, logger *slog.Logger) (*Service, error) {
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
		acsURL:      *base.JoinPath("/auth/saml/acs"),
		metadataURL: *base.JoinPath("/auth/saml/metadata"),
		frontendURL: *frontend,
		metadataSrc: *metadataSrc,
		httpClient:  httpClient,
		relay:       NewRelayState(cfg.RelayStateSecret),
		store:       store,
		sessions:    sessions,
		logger:      logger,
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
func (s *Service) sp() saml.ServiceProvider {
	return saml.ServiceProvider{
		EntityID:          s.metadataURL.String(),
		MetadataURL:       s.metadataURL,
		AcsURL:            s.acsURL,
		IDPMetadata:       s.idpMetadata.Load(),
		AuthnNameIDFormat: saml.EmailAddressNameIDFormat,
		// Responses are accepted without a tracked request ID (no
		// server-side AuthnRequest state), matching the GSH behavior.
		AllowIDPInitiated: true,
	}
}

// Register mounts the SAML routes. They must NOT require a session.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/saml/init", s.handleInit)
	mux.HandleFunc("POST /auth/saml/acs", s.handleACS)
	mux.HandleFunc("GET /auth/saml/metadata", s.handleMetadata)
}

func (s *Service) handleInit(w http.ResponseWriter, r *http.Request) {
	sp := s.sp()
	req, err := sp.MakeAuthenticationRequest(
		sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		s.logger.Error("failed to build AuthnRequest", "error", err)
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
		s.logger.Warn("SAML response rejected", "reason", "form parse failed or body too large")
		http.Error(w, "SAML validation failed", http.StatusUnauthorized)
		return
	}

	sp := s.sp()
	assertion, err := sp.ParseResponse(r, nil)
	if err != nil {
		s.logSAMLError(err)
		s.countSignIn(false)
		http.Error(w, "SAML validation failed", http.StatusUnauthorized)
		return
	}

	subject := ""
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		subject = assertion.Subject.NameID.Value
	}
	if subject == "" {
		s.logger.Warn("SAML assertion without NameID")
		s.countSignIn(false)
		http.Error(w, "SAML validation failed", http.StatusUnauthorized)
		return
	}

	// NameID doubles as subject and email until the Okta app exposes a
	// dedicated stable userId attribute (open question in the design).
	user, err := identity.Resolve(r.Context(), s.store, identity.ProviderOkta, subject, subject)
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

func (s *Service) handleMetadata(w http.ResponseWriter, _ *http.Request) {
	sp := s.sp()
	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		http.Error(w, "metadata unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(out)
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
