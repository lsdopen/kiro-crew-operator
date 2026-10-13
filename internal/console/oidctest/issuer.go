// Package oidctest is an in-memory OIDC issuer for tests, so the real go-oidc
// verification path runs without a network.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ClientID is the audience the issuer signs for by default.
const ClientID = "test-client"

type pending struct {
	claims    map[string]any
	challenge string
}

// Issuer serves discovery, JWKS and a token endpoint.
type Issuer struct {
	// URL is the issuer identifier.
	URL    string
	server *httptest.Server
	mux    *http.ServeMux
	key    *rsa.PrivateKey
	signer jose.Signer
	// Forge makes the token endpoint sign with a key the JWKS does not publish.
	Forge bool

	mu    sync.Mutex
	codes map[string]pending
}

// New starts an issuer. Call Close when done.
func New() (*Issuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return nil, err
	}
	i := &Issuer{key: key, signer: signer, codes: map[string]pending{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", i.discovery)
	mux.HandleFunc("/keys", i.keys)
	mux.HandleFunc("/token", i.token)
	i.mux = mux
	i.server = httptest.NewServer(mux)
	i.URL = i.server.URL
	return i, nil
}

// Handle adds a route, such as an /authorize page the tests do not need.
func (i *Issuer) Handle(pattern string, h http.HandlerFunc) { i.mux.HandleFunc(pattern, h) }

// Close stops the issuer.
func (i *Issuer) Close() { i.server.Close() }

// Authorize registers a code for the authorization URL the console built. Keys in
// overrides replace the default claims; a nil value deletes the claim.
func (i *Issuer) Authorize(authURL string, overrides map[string]any) (code string, err error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	claims := map[string]any{
		"iss":            i.URL,
		"aud":            q.Get("client_id"),
		"sub":            "1234",
		"email":          "alice@example.com",
		"email_verified": true,
		"hd":             "example.com",
		"nonce":          q.Get("nonce"),
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code = base64.RawURLEncoding.EncodeToString(buf)
	i.mu.Lock()
	i.codes[code] = pending{claims: claims, challenge: q.Get("code_challenge")}
	i.mu.Unlock()
	return code, nil
}

func (i *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                i.URL,
		"authorization_endpoint":                i.URL + "/authorize",
		"token_endpoint":                        i.URL + "/token",
		"jwks_uri":                              i.URL + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (i *Issuer) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &i.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
	}})
}

func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	i.mu.Lock()
	p, ok := i.codes[r.PostForm.Get("code")]
	delete(i.codes, r.PostForm.Get("code"))
	i.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	signer := i.signer
	if i.Forge {
		other, kerr := rsa.GenerateKey(rand.Reader, 2048)
		if kerr != nil {
			http.Error(w, kerr.Error(), http.StatusInternalServerError)
			return
		}
		signer, _ = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: other, KeyID: "k1"}}, nil)
	}
	raw, err := jwt.Signed(signer).Claims(p.claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
