package auth

import (
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
	CookieKey    []byte
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
	s.CookieKey = []byte(key)
	return &s, nil
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
