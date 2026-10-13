// Package web serves the console: login, sessions, CSRF and the read-only views.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/oauth2"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
)

//go:embed static
var staticFS embed.FS

// refreshAfter is how stale a session's Seen may get before it is re-sealed.
const refreshAfter = 5 * time.Minute

// crewsPath is the landing page and the fallback for an unsafe post-login return.
const crewsPath = "/crews"

// Server wires the handlers. All fields are required.
type Server struct {
	Provider       auth.Provider
	Sealer         *auth.Sealer
	Store          *authz.Store
	AllowedDomains []string
	Log            logr.Logger

	writeLimit *limiter
}

// maxFormBytes caps a posted form.
const maxFormBytes = 16 << 10

type authedHandler func(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount)

// Handler is the full console, with security headers and Origin checks applied.
func (s *Server) Handler() http.Handler {
	s.writeLimit = newLimiter(writeBurst, writeEvery)
	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.authed(s.logout))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, crewsPath, http.StatusSeeOther)
	})
	mux.HandleFunc("GET /crews", s.authed(s.page("crews")))
	mux.HandleFunc("GET /requests", s.authed(s.page("requests")))
	mux.HandleFunc("GET /accounts", s.authed(s.page("accounts")))
	mux.HandleFunc("POST /requests", s.authed(s.createRequest))
	mux.HandleFunc("POST /requests/{name}/decision", s.authed(s.decideRequest))
	mux.HandleFunc("POST /requests/{name}/cancel", s.authed(s.cancelRequest))

	// CrossOriginProtection rejects cross-origin unsafe requests by Origin and
	// Sec-Fetch-Site, in addition to the CSRF token.
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	st := &auth.OAuthState{
		State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(),
		Return: safeReturn(r.URL.Query().Get("return")),
	}
	c, err := s.Sealer.OAuthCookie(st)
	if err != nil {
		s.Log.Error(err, "sealing login state")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	hint := ""
	if domains := auth.NormalizeDomains(s.AllowedDomains); len(domains) == 1 {
		hint = domains[0]
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, s.Provider.AuthURL(st.State, st.Nonce, st.Verifier, hint), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sealer.OpenOAuth(r)
	// The login state is single use whatever happens next.
	http.SetCookie(w, auth.ClearCookie(auth.OAuthCookie))
	if err != nil {
		http.Error(w, "login expired, try again", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("code") == "" || !constEq(q.Get("state"), st.State) {
		s.Log.Info("login callback rejected", "providerError", q.Get("error"))
		http.Error(w, "login failed", http.StatusBadRequest)
		return
	}
	claims, err := s.Provider.Exchange(r.Context(), q.Get("code"), st.Verifier)
	if err != nil {
		s.Log.Info("id token rejected", "reason", err.Error())
		http.Error(w, "sign-in refused", http.StatusForbidden)
		return
	}
	if !constEq(claims.Nonce, st.Nonce) {
		s.Log.Info("id token rejected", "reason", "nonce mismatch")
		http.Error(w, "sign-in refused", http.StatusForbidden)
		return
	}
	email, err := auth.CheckClaims(*claims, s.AllowedDomains)
	if err != nil {
		s.Log.Info("sign-in refused", "reason", err.Error())
		http.Error(w, "sign-in refused", http.StatusForbidden)
		return
	}
	if _, err := s.Store.Resolve(r.Context(), email); err != nil {
		s.Log.Error(err, "resolving account")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.startSession(w, email); err != nil {
		s.Log.Error(err, "creating session")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, st.Return, http.StatusSeeOther)
}

func (s *Server) startSession(w http.ResponseWriter, email string) error {
	sess, err := s.Sealer.NewSession(email)
	if err != nil {
		return err
	}
	c, err := s.Sealer.SessionCookie(sess)
	if err != nil {
		return err
	}
	http.SetCookie(w, c)
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ *auth.Session, _ *v1alpha1.KiroCrewAccount) {
	http.SetCookie(w, auth.ClearCookie(auth.SessionCookie))
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// authed requires a valid session and an existing account, and CSRF on unsafe
// methods. The account is re-read on every request, so role changes apply at once.
func (s *Server) authed(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Sealer.OpenSession(r)
		if err != nil {
			s.denyUnauthenticated(w, r, false)
			return
		}
		acct, err := s.Store.Find(r.Context(), sess.Email)
		if err != nil {
			s.Log.Error(err, "looking up account")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if acct == nil {
			s.denyUnauthenticated(w, r, true)
			return
		}
		if !safeMethod(r.Method) {
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
			_ = r.ParseForm()
			if !s.csrfOK(r, sess) {
				s.fail(w, r, http.StatusForbidden, "invalid CSRF token")
				return
			}
			if !s.writeLimit.allow(sess.SID) {
				w.Header().Set("Retry-After", retryAfterS)
				s.fail(w, r, http.StatusTooManyRequests, "too many requests, slow down")
				return
			}
		}
		if s.Sealer.Now().Sub(sess.Seen) > refreshAfter {
			sess.Seen = s.Sealer.Now()
			if c, err := s.Sealer.SessionCookie(sess); err == nil {
				http.SetCookie(w, c)
			}
		}
		next(w, r, sess, acct)
	}
}

func (s *Server) denyUnauthenticated(w http.ResponseWriter, r *http.Request, clear bool) {
	if clear {
		http.SetCookie(w, auth.ClearCookie(auth.SessionCookie))
	}
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/auth/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	target := "/auth/login"
	if r.Method == http.MethodGet {
		target += "?return=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) csrfOK(r *http.Request, sess *auth.Session) bool {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		_ = r.ParseForm()
		token = r.PostForm.Get("csrf_token")
	}
	return s.Sealer.CSRFValid(sess.SID, token)
}

func (s *Server) page(active string) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
		if active == "accounts" && acct.Spec.Role != v1alpha1.RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.render(w, r, sess, acct, active, "")
	}
}

// render writes a page, or its content fragment for htmx. notice is shown on it.
func (s *Server) render(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount, active, notice string) {
	view, err := s.Store.Visible(r.Context(), acct)
	if err != nil {
		s.Log.Error(err, "listing resources")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p := newPage(view, s.Sealer.CSRFToken(sess.SID), active)
	p.Notice = notice
	w.Header().Set("Vary", "HX-Request")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	c := Layout(p)
	if isHTMX(r) {
		c = Content(p)
	}
	if err := c.Render(r.Context(), w); err != nil {
		s.Log.Error(err, "rendering page")
	}
}

// fail answers an error. htmx gets the message swapped into #flash, since it does
// not swap error responses into the page by default.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if !isHTMX(r) {
		http.Error(w, msg, status)
		return
	}
	h := w.Header()
	h.Set("HX-Retarget", "#flash")
	h.Set("HX-Reswap", "innerHTML")
	h.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := Flash(msg).Render(r.Context(), w); err != nil {
		s.Log.Error(err, "rendering message")
	}
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func constEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeReturn keeps the post-login redirect on this site. Browsers strip tabs and
// newlines from a Location, so "/\t/evil.com" would otherwise become "//evil.com".
func safeReturn(p string) string {
	if hasControlOrBackslash(p) || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return crewsPath
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || hasControlOrBackslash(u.Path) {
		return crewsPath
	}
	return p
}

func hasControlOrBackslash(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return true
		}
	}
	return false
}
