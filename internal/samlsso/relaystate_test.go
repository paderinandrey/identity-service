package samlsso

import (
	"testing"
	"time"
)

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/orders/42", "/orders/42"},
		{"/a?b=c#d", "/a?b=c#d"},
		{"", "/"},
		{"   ", "/"},
		{"https://evil.example.com/", "/"},
		{"HTTPS://evil.example.com", "/"},
		{"//evil.example.com/path", "/"},
		{"javascript://alert(1)", "/"},
		{"relative/path", "/"},
		{"/line\nbreak", "/"},
		{"/line\rbreak", "/"},
	}
	for _, tt := range tests {
		if got := SanitizePath(tt.in); got != tt.want {
			t.Errorf("SanitizePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRelayStateRoundTrip(t *testing.T) {
	rs := NewRelayState("secret")
	token := rs.Encode("/orders/42?tab=docs")
	if got := rs.Decode(token); got != "/orders/42?tab=docs" {
		t.Errorf("Decode = %q", got)
	}
}

func TestRelayStateEncodesSanitized(t *testing.T) {
	rs := NewRelayState("secret")
	if got := rs.Decode(rs.Encode("https://evil.example.com")); got != "/" {
		t.Errorf("Decode of unsafe path = %q, want /", got)
	}
}

func TestRelayStateRejectsTampering(t *testing.T) {
	rs := NewRelayState("secret")
	token := rs.Encode("/ok")

	if got := rs.Decode(token + "x"); got != "/" {
		t.Errorf("tampered signature: Decode = %q, want /", got)
	}
	if got := rs.Decode("garbage"); got != "/" {
		t.Errorf("malformed token: Decode = %q, want /", got)
	}
	if got := rs.Decode(""); got != "/" {
		t.Errorf("empty token: Decode = %q, want /", got)
	}
	if got := NewRelayState("other-secret").Decode(token); got != "/" {
		t.Errorf("wrong secret: Decode = %q, want /", got)
	}
}

func TestRelayStateExpires(t *testing.T) {
	rs := NewRelayState("secret")
	issued := time.Now()
	rs.now = func() time.Time { return issued }
	token := rs.Encode("/ok")

	rs.now = func() time.Time { return issued.Add(relayTTL + time.Second) }
	if got := rs.Decode(token); got != "/" {
		t.Errorf("expired token: Decode = %q, want /", got)
	}

	rs.now = func() time.Time { return issued.Add(relayTTL - time.Second) }
	if got := rs.Decode(token); got != "/ok" {
		t.Errorf("fresh token: Decode = %q, want /ok", got)
	}
}
