package web

import (
	"errors"
	"net/http"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
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
