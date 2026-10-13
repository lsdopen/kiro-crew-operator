package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestSealer(t *testing.T, key string) *Sealer {
	t.Helper()
	s, err := NewSealer([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	testKey = "0123456789abcdef0123456789abcdef"
	// base64 of testKey (32 bytes).
	testKeyB64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
)

func reqWith(c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	return r
}

func TestSessionRoundTripAndCookieAttributes(t *testing.T) {
	s := newTestSealer(t, testKey)
	sess, _ := s.NewSession(aliceEmail)
	c, err := s.SessionCookie(sess)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "__Host-kc_session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/" || c.Domain != "" || c.Expires.IsZero() {
		t.Fatalf("bad cookie attributes: %+v", c)
	}
	if strings.Contains(c.Value, "alice") {
		t.Fatal("session payload is readable in the cookie")
	}
	got, err := s.OpenSession(reqWith(c))
	if err != nil || got.Email != aliceEmail || got.SID != sess.SID {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestSessionRejects(t *testing.T) {
	s := newTestSealer(t, testKey)
	sess, _ := s.NewSession(aliceEmail)
	c, _ := s.SessionCookie(sess)

	t.Run("tampered", func(t *testing.T) {
		bad := *c
		b := []byte(bad.Value)
		b[len(b)/2] ^= 1
		bad.Value = string(b)
		if _, err := s.OpenSession(reqWith(&bad)); err == nil {
			t.Fatal("accepted a tampered cookie")
		}
	})
	t.Run("other key", func(t *testing.T) {
		other := newTestSealer(t, "ffffffffffffffffffffffffffffffff")
		if _, err := other.OpenSession(reqWith(c)); err == nil {
			t.Fatal("accepted a cookie sealed with another key")
		}
	})
	t.Run("oauth cookie replayed as session", func(t *testing.T) {
		oc, _ := s.OAuthCookie(&OAuthState{State: "s"})
		replay := *oc
		replay.Name = SessionCookie
		if _, err := s.OpenSession(reqWith(&replay)); err == nil {
			t.Fatal("accepted an oauth cookie as a session")
		}
	})
	t.Run("absolute expiry", func(t *testing.T) {
		s.SetClock(func() time.Time { return sess.IAT.Add(SessionLifetime + time.Second) })
		defer s.SetClock(time.Now)
		if _, err := s.OpenSession(reqWith(c)); err == nil {
			t.Fatal("accepted an expired session")
		}
	})
	t.Run("idle expiry", func(t *testing.T) {
		s.SetClock(func() time.Time { return sess.Seen.Add(SessionIdle + time.Second) })
		defer s.SetClock(time.Now)
		if _, err := s.OpenSession(reqWith(c)); err == nil {
			t.Fatal("accepted an idle session")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := s.OpenSession(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
			t.Fatal("accepted no cookie")
		}
	})
}

func TestOAuthStateExpires(t *testing.T) {
	s := newTestSealer(t, testKey)
	c, _ := s.OAuthCookie(&OAuthState{State: "s", Nonce: "n"})
	if got, err := s.OpenOAuth(reqWith(c)); err != nil || got.Nonce != "n" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	s.SetClock(func() time.Time { return time.Now().Add(OAuthLifetime + time.Second) })
	if _, err := s.OpenOAuth(reqWith(c)); err == nil {
		t.Fatal("accepted an expired oauth state")
	}
}

func TestCSRFBoundToSession(t *testing.T) {
	s := newTestSealer(t, testKey)
	tok := s.CSRFToken("sid-1")
	if !s.CSRFValid("sid-1", tok) {
		t.Fatal("own token rejected")
	}
	if s.CSRFValid("sid-2", tok) || s.CSRFValid("sid-1", "") || s.CSRFValid("sid-1", tok+"0") {
		t.Fatal("accepted a wrong token")
	}
	if newTestSealer(t, "ffffffffffffffffffffffffffffffff").CSRFValid("sid-1", tok) {
		t.Fatal("token valid under another key")
	}
}

func TestNewSealerShortKey(t *testing.T) {
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Fatal("accepted a short key")
	}
}

func TestLoadSecretsCookieKey(t *testing.T) {
	std := base64.StdEncoding.EncodeToString([]byte(testKey + "??>>"))
	url := base64.URLEncoding.EncodeToString([]byte(testKey + "??>>"))
	if std == url {
		t.Fatal("test key must differ between alphabets")
	}
	tests := []struct {
		name, key string
		ok        bool
	}{
		{"std base64, 32 bytes", testKeyB64, true},
		{"std alphabet", std, true},
		{"url alphabet", url, true},
		{"31 bytes", base64.StdEncoding.EncodeToString([]byte(testKey[:31])), false},
		{"raw 32-char passphrase", "0123456789abcdef0123456789abcde", false},
		{"not base64", "not base64 at all!!", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, v := range map[string]string{"client-id": "id", "client-secret": "s", "cookie-key": tt.key} {
				if err := os.WriteFile(filepath.Join(dir, n), []byte(v), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s, err := LoadSecrets(dir)
			if tt.ok && (err != nil || len(s.CookieKey) < MinKeyLen) {
				t.Fatalf("rejected: %v", err)
			}
			if !tt.ok && (err == nil || !strings.Contains(err.Error(), "openssl rand -base64 32")) {
				t.Fatalf("err = %v, want a refusal naming openssl", err)
			}
		})
	}
}

func TestLoadSecrets(t *testing.T) {
	dir := t.TempDir()
	write := func(name, v string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("client-id", "id\n")
	write("client-secret", "sec")
	if _, err := LoadSecrets(dir); err == nil {
		t.Fatal("missing cookie-key accepted")
	}
	write("cookie-key", testKeyB64+"\n")
	s, err := LoadSecrets(dir)
	if err != nil || s.ClientID != "id" || s.ClientSecret != "sec" || string(s.CookieKey) != testKey {
		t.Fatalf("got %+v %v", s, err)
	}
	write("client-secret", "\n")
	if _, err := LoadSecrets(dir); err == nil {
		t.Fatal("empty secret accepted")
	}
}
