package samlsso

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// relayTTL bounds how long a RelayState token stays valid (mirrors GSH).
const relayTTL = 5 * time.Minute

var absoluteOrSchemeRelative = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*:)?//`)

// SanitizePath keeps only safe app-relative paths; anything else becomes "/".
func SanitizePath(p string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "",
		absoluteOrSchemeRelative.MatchString(p),
		!strings.HasPrefix(p, "/"),
		strings.ContainsAny(p, "\r\n"):
		return "/"
	}
	return p
}

// RelayState signs and verifies the post-login return path with an
// HMAC-SHA256 token carrying an issue timestamp.
type RelayState struct {
	secret []byte
	now    func() time.Time
}

// NewRelayState builds a codec with the shared signing secret.
func NewRelayState(secret string) *RelayState {
	return &RelayState{secret: []byte(secret), now: time.Now}
}

type relayPayload struct {
	Path string `json:"path"`
	TS   int64  `json:"ts"`
}

// Encode produces a signed token for the sanitized path.
func (r *RelayState) Encode(path string) string {
	body, _ := json.Marshal(relayPayload{Path: SanitizePath(path), TS: r.now().Unix()})
	encoded := base64.RawURLEncoding.EncodeToString(body)
	return encoded + "." + r.sign(encoded)
}

// Decode returns the stored path, or "/" for missing, tampered or expired
// tokens: a bad RelayState must not break the login itself.
func (r *RelayState) Decode(token string) string {
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(r.sign(encoded)), []byte(sig)) {
		return "/"
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "/"
	}
	var payload relayPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return "/"
	}
	issued := time.Unix(payload.TS, 0)
	if payload.TS <= 0 || r.now().Sub(issued) > relayTTL {
		return "/"
	}
	return SanitizePath(payload.Path)
}

func (r *RelayState) sign(encoded string) string {
	mac := hmac.New(sha256.New, r.secret)
	mac.Write([]byte(encoded))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
