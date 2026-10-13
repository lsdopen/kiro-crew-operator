package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Cookie names. The __Host- prefix makes browsers require Secure, Path=/ and no
// Domain, so a sibling subdomain cannot plant or overwrite them.
const (
	SessionCookie = "__Host-kc_session"
	OAuthCookie   = "__Host-kc_oauth"

	// MinKeyLen is the shortest cookie key accepted.
	MinKeyLen = 32

	// SessionLifetime is the absolute lifetime; SessionIdle the sliding one.
	SessionLifetime = 8 * time.Hour
	SessionIdle     = time.Hour
	// OAuthLifetime bounds how long a login may take.
	OAuthLifetime = 10 * time.Minute
)

// ErrInvalidCookie covers every way a cookie can fail to open: tampering, a
// wrong key, truncation, or expiry. Callers must not distinguish them.
var ErrInvalidCookie = errors.New("invalid cookie")

// Session is the sealed session payload. There is no server-side store.
type Session struct {
	Email string    `json:"email"`
	IAT   time.Time `json:"iat"`
	EXP   time.Time `json:"exp"`
	// Seen is the last activity, for the sliding idle timeout.
	Seen time.Time `json:"seen"`
	SID  string    `json:"sid"`
}

// Valid reports whether the session is inside both its absolute and idle windows.
func (s *Session) Valid(now time.Time) bool {
	return now.Before(s.EXP) && now.Before(s.Seen.Add(SessionIdle))
}

// OAuthState is the transient login state held between redirect and callback.
type OAuthState struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	Return   string    `json:"return"`
	EXP      time.Time `json:"exp"`
}

// Sealer encrypts and authenticates cookie payloads with AES-256-GCM, and
// derives the CSRF key from the same root key.
type Sealer struct {
	aead cipher.AEAD
	csrf []byte
	now  func() time.Time
}

// NewSealer derives independent subkeys from key with HKDF.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) < MinKeyLen {
		return nil, fmt.Errorf("cookie key must be at least %d bytes, got %d", MinKeyLen, len(key))
	}
	enc, err := hkdf.Key(sha256.New, key, nil, "kiro-crew-console cookie encryption", 32)
	if err != nil {
		return nil, err
	}
	csrf, err := hkdf.Key(sha256.New, key, nil, "kiro-crew-console csrf", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(enc)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead, csrf: csrf, now: time.Now}, nil
}

// SetClock replaces the time source, for tests.
func (s *Sealer) SetClock(now func() time.Time) { s.now = now }

// Now returns the sealer's current time.
func (s *Sealer) Now() time.Time { return s.now() }

// seal binds the ciphertext to the cookie name, so an OAuth cookie cannot be
// replayed as a session cookie.
func (s *Sealer) seal(name string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plain, []byte(name))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

func (s *Sealer) open(name, value string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return ErrInvalidCookie
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return ErrInvalidCookie
	}
	if err := json.Unmarshal(plain, v); err != nil {
		return ErrInvalidCookie
	}
	return nil
}

// NewSession starts a session for email.
func (s *Sealer) NewSession(email string) (*Session, error) {
	sid := make([]byte, 16)
	if _, err := rand.Read(sid); err != nil {
		return nil, err
	}
	now := s.now()
	return &Session{
		Email: email, IAT: now, Seen: now, EXP: now.Add(SessionLifetime),
		SID: hex.EncodeToString(sid),
	}, nil
}

// SessionCookie seals sess into a cookie that lives until the absolute expiry.
func (s *Sealer) SessionCookie(sess *Session) (*http.Cookie, error) {
	v, err := s.seal(SessionCookie, sess)
	if err != nil {
		return nil, err
	}
	return secureCookie(SessionCookie, v, sess.EXP), nil
}

// OpenSession returns the session in r, or ErrInvalidCookie if it is absent,
// forged, or outside its absolute or idle window.
func (s *Sealer) OpenSession(r *http.Request) (*Session, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, ErrInvalidCookie
	}
	var sess Session
	if err := s.open(SessionCookie, c.Value, &sess); err != nil {
		return nil, err
	}
	if !sess.Valid(s.now()) {
		return nil, ErrInvalidCookie
	}
	return &sess, nil
}

// OAuthCookie seals the transient login state.
func (s *Sealer) OAuthCookie(st *OAuthState) (*http.Cookie, error) {
	st.EXP = s.now().Add(OAuthLifetime)
	v, err := s.seal(OAuthCookie, st)
	if err != nil {
		return nil, err
	}
	return secureCookie(OAuthCookie, v, st.EXP), nil
}

// OpenOAuth returns the login state in r, or ErrInvalidCookie.
func (s *Sealer) OpenOAuth(r *http.Request) (*OAuthState, error) {
	c, err := r.Cookie(OAuthCookie)
	if err != nil {
		return nil, ErrInvalidCookie
	}
	var st OAuthState
	if err := s.open(OAuthCookie, c.Value, &st); err != nil {
		return nil, err
	}
	if !s.now().Before(st.EXP) {
		return nil, ErrInvalidCookie
	}
	return &st, nil
}

// ClearCookie expires the named cookie.
func ClearCookie(name string) *http.Cookie {
	c := secureCookie(name, "", time.Unix(0, 0))
	c.MaxAge = -1
	return c
}

// CSRFToken is HMAC(key, sid), so it is bound to one session and needs no store.
func (s *Sealer) CSRFToken(sid string) string {
	m := hmac.New(sha256.New, s.csrf)
	m.Write([]byte(sid))
	return hex.EncodeToString(m.Sum(nil))
}

// CSRFValid compares token to the session's token in constant time.
func (s *Sealer) CSRFValid(sid, token string) bool {
	return hmac.Equal([]byte(s.CSRFToken(sid)), []byte(token))
}

func secureCookie(name, value string, exp time.Time) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: exp,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}
