package auth_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/oidctest"
)

func newProvider(t *testing.T) (*auth.OIDC, *oidctest.Issuer) {
	t.Helper()
	iss, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(iss.Close)
	p, err := auth.NewOIDC(context.Background(), auth.OIDCConfig{
		IssuerURL: iss.URL, RedirectURL: "https://console.example.com/auth/callback",
		ClientID: oidctest.ClientID, ClientSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, iss
}

func TestOIDCExchange(t *testing.T) {
	const verifier = "v-0123456789012345678901234567890123456789012"
	tests := []struct {
		name      string
		overrides map[string]any
		forge     bool
		verifier  string
		wantErr   bool
	}{
		{name: "valid", verifier: verifier},
		{name: "wrong audience", overrides: map[string]any{"aud": "someone-else"}, verifier: verifier, wantErr: true},
		{name: "wrong issuer", overrides: map[string]any{"iss": "https://evil.example.com"}, verifier: verifier,
			wantErr: true},
		{name: "expired", overrides: map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}, verifier: verifier,
			wantErr: true},
		{name: "signed by an unpublished key", forge: true, verifier: verifier, wantErr: true},
		{name: "PKCE verifier mismatch", verifier: "another-verifier-0123456789012345678901234", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, iss := newProvider(t)
			iss.Forge = tt.forge
			authURL := p.AuthURL("st", "nonce-1", verifier, "example.com")
			u, _ := url.Parse(authURL)
			q := u.Query()
			if q.Get("code_challenge_method") != "S256" || q.Get("nonce") != "nonce-1" || q.Get("state") != "st" ||
				q.Get("hd") != "example.com" {
				t.Fatalf("auth URL missing PKCE/state/nonce/hd: %s", authURL)
			}
			code, err := iss.Authorize(authURL, tt.overrides)
			if err != nil {
				t.Fatal(err)
			}
			c, err := p.Exchange(context.Background(), code, tt.verifier)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (c.Email != "alice@example.com" || c.HostedDomain != "example.com" ||
				!c.EmailVerified || c.Nonce != "nonce-1") {
				t.Fatalf("claims = %+v", c)
			}
		})
	}
}
