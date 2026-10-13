package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
	"github.com/lsdopen/kiro-crew-operator/internal/console/broker"
)

// Opener returns the URL that signs a crew's owner in to its dashboard.
type Opener interface {
	Location(ctx context.Context, crew *v1alpha1.KiroCrew) (string, error)
}

// Open is limited to 6 a minute per session, on top of the write limit.
const (
	openBurst = 6
	openEvery = 10 * time.Second
)

func (s *Server) startCrew(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	s.setRunning(w, r, sess, acct, true, "Crew starting.")
}

func (s *Server) stopCrew(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	s.setRunning(w, r, sess, acct, false, "Crew stopping.")
}

// setRunning takes only the crew name from the path: the patch is fixed by the action.
func (s *Server) setRunning(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount, start bool, notice string) {
	if err := s.Store.SetRunning(r.Context(), acct, r.PathValue("name"), start, s.Sealer.Now()); err != nil {
		s.crewFailed(w, r, err)
		return
	}
	if !isHTMX(r) {
		http.Redirect(w, r, crewsPath, http.StatusSeeOther)
		return
	}
	s.render(w, r, sess, acct, "crews", notice)
}

// editSchedule reads only up, down and timezone: the patch is fixed to the schedule field.
func (s *Server) editSchedule(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	f := r.PostForm
	if err := s.Store.SetSchedule(r.Context(), acct, r.PathValue("name"), f.Get("up"), f.Get("down"), f.Get("timezone")); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "crews", "Schedule saved.")
}

// editMCP reads only the ConfigMap name, which must be on the config's allowlist.
func (s *Server) editMCP(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	if err := s.Store.SetMCPConfig(r.Context(), acct, r.PathValue("name"), r.PostForm.Get("mcp_config_ref")); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "crews", "MCP config saved; the crew restarts.")
}

// resizeCrew files a resize request from size and reason only. The console never writes the crew's size.
func (s *Server) resizeCrew(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	q, err := s.Store.RequestResize(r.Context(), acct, r.PathValue("name"), r.PostForm.Get("size"), r.PostForm.Get("reason"))
	if err != nil {
		s.writeFailed(w, r, err)
		return
	}
	notice := "Resize requested. Once approved it applies at once and restarts the crew."
	if q.Spec.Decision != nil {
		notice = "Resize approved. The crew restarts as it applies."
	}
	s.done(w, r, sess, acct, "crews", notice)
}

// openCrew answers 303 to the crew's dashboard with a fresh sign-in token, for
// the crew's owner only. The token is in the Location header and nowhere else:
// not in the body, and not in any log line.
func (s *Server) openCrew(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	if s.Broker == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "sign-in is not enabled on this console")
		return
	}
	if !s.openLimit.allow(sess.SID) {
		w.Header().Set("Retry-After", retryAfterS)
		s.fail(w, r, http.StatusTooManyRequests, "too many requests, slow down")
		return
	}
	crew, err := s.Store.OwnedCrew(r.Context(), acct, r.PathValue("name"))
	if err != nil {
		s.crewFailed(w, r, err)
		return
	}
	loc, err := s.Broker.Location(r.Context(), crew)
	switch {
	case errors.Is(err, broker.ErrNotReady):
		s.fail(w, r, http.StatusConflict, "the crew is not ready to open yet")
		return
	case err != nil:
		// Broker errors never carry the token or the command output.
		s.Log.Error(err, "signing in to crew", "crew", crew.Name)
		s.fail(w, r, http.StatusBadGateway, "could not sign in to the crew")
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Location", loc)
	w.WriteHeader(http.StatusSeeOther)
}

func (s *Server) crewFailed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, authz.ErrForbidden):
		s.fail(w, r, http.StatusForbidden, "forbidden")
	case errors.Is(err, authz.ErrCrewNotFound):
		s.fail(w, r, http.StatusNotFound, "crew not found")
	case errors.Is(err, authz.ErrNoNamespace):
		s.fail(w, r, http.StatusServiceUnavailable, "crews are not enabled on this console")
	default:
		s.Log.Error(err, "writing crew")
		s.fail(w, r, http.StatusInternalServerError, "internal error")
	}
}
