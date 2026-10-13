package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Provider runs the authorization code flow. It is an interface so web tests can
// drive the handlers without a network.
type Provider interface {
	// AuthURL is where the browser is sent to sign in.
	AuthURL(state, nonce, verifier string, hostedDomainHint string) string
	// Exchange redeems the code and returns the verified ID token's claims.
	// The signature, iss, aud and exp are checked here; nonce and policy are not.
	Exchange(ctx context.Context, code, verifier string) (*Claims, error)
}

// OIDCConfig configures the real provider.
type OIDCConfig struct {
	IssuerURL    string
	RedirectURL  string
	ClientID     string
	ClientSecret string
}

// OIDC is the go-oidc backed Provider.
type OIDC struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOIDC fetches the issuer's discovery document, so it needs network access.
func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	p, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discovering %s: %w", cfg.IssuerURL, err)
	}
	return &OIDC{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     p.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

// AuthURL builds the PKCE (S256) authorization URL. The hd hint only steers
// Google's account chooser and is never trusted.
func (o *OIDC) AuthURL(state, nonce, verifier, hostedDomainHint string) string {
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	if hostedDomainHint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("hd", hostedDomainHint))
	}
	return o.oauth.AuthCodeURL(state, opts...)
}

// Exchange redeems the code with the PKCE verifier and verifies the ID token.
func (o *OIDC) Exchange(ctx context.Context, code, verifier string) (*Claims, error) {
	tok, err := o.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("token response has no id_token")
	}
	idToken, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verifying id token: %w", err)
	}
	var c Claims
	if err := idToken.Claims(&c); err != nil {
		return nil, fmt.Errorf("decoding claims: %w", err)
	}
	return &c, nil
}
