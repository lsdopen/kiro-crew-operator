package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Secrets are read from files in a mounted Secret, never from flags or env.
type Secrets struct {
	ClientID     string
	ClientSecret string
	// CookieKey is the base64-decoded cookie-key, at least MinKeyLen bytes.
	CookieKey []byte
}

// LoadSecrets reads client-id, client-secret and cookie-key from dir.
func LoadSecrets(dir string) (*Secrets, error) {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" {
			return "", fmt.Errorf("%s is empty", name)
		}
		return v, nil
	}
	var s Secrets
	var err error
	if s.ClientID, err = read("client-id"); err != nil {
		return nil, err
	}
	if s.ClientSecret, err = read("client-secret"); err != nil {
		return nil, err
	}
	key, err := read("cookie-key")
	if err != nil {
		return nil, err
	}
	if s.CookieKey, err = decodeCookieKey(key); err != nil {
		return nil, err
	}
	return &s, nil
}

// decodeCookieKey requires base64 (standard or URL alphabet) of at least
// MinKeyLen random bytes, so a typed passphrase cannot be used as the key.
func decodeCookieKey(v string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(v); err == nil {
			if len(b) < MinKeyLen {
				return nil, fmt.Errorf("cookie-key decodes to %d bytes, need at least %d; "+
					"generate one with: openssl rand -base64 32", len(b), MinKeyLen)
			}
			return b, nil
		}
	}
	return nil, errors.New("cookie-key is not valid base64; generate one with: openssl rand -base64 32")
}

// ValidateDomains makes an empty allow list a startup error, so the
// misconfiguration is loud. allowEmpty is for tests only: an empty list still
// refuses every login.
func ValidateDomains(domains []string, allowEmpty bool) error {
	if len(NormalizeDomains(domains)) == 0 && !allowEmpty {
		return errors.New("auth.allowedDomains is empty: every login would be refused; " +
			"set it, or set auth.allowEmptyDomains to start anyway")
	}
	return nil
}
