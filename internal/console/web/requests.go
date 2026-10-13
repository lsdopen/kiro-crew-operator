package web

import (
	"errors"
	"net/http"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
)

// createRequest files a request for the session account. It reads only size,
// schedule and reason from the form: the requester is always the session email.
func (s *Server) createRequest(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	in := authz.NewRequest{
		Size: r.PostForm.Get("size"), Up: r.PostForm.Get("up"), Down: r.PostForm.Get("down"),
		Zone: r.PostForm.Get("timezone"), Reason: r.PostForm.Get("reason"),
	}
	if _, err := s.Store.CreateRequest(r.Context(), acct, in); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "Request submitted.")
}

// decideRequest reads only the verdict from the form: decision.by is the session email.
func (s *Server) decideRequest(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	verdict := v1alpha1.Verdict(r.PostForm.Get("verdict"))
	if err := s.Store.Decide(r.Context(), acct, r.PathValue("name"), verdict); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "Decision recorded.")
}

func (s *Server) cancelRequest(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	if err := s.Store.Cancel(r.Context(), acct, r.PathValue("name")); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "Request cancelled.")
}

// done answers a successful write: the refreshed requests view for htmx, or a
// redirect back to it.
func (s *Server) done(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount, notice string) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/requests", http.StatusSeeOther)
		return
	}
	s.render(w, r, sess, acct, "requests", notice)
}

func (s *Server) writeFailed(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *authz.InvalidError
	switch {
	case errors.As(err, &invalid):
		s.fail(w, r, http.StatusBadRequest, invalid.Msg)
	case errors.Is(err, authz.ErrForbidden):
		s.fail(w, r, http.StatusForbidden, "forbidden")
	case errors.Is(err, authz.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "request not found")
	case errors.Is(err, authz.ErrNotPending), errors.Is(err, authz.ErrConflict),
		errors.Is(err, authz.ErrOpenRequest), errors.Is(err, authz.ErrHasCrew):
		s.fail(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, authz.ErrNoNamespace):
		s.fail(w, r, http.StatusServiceUnavailable, "requests are not enabled on this console")
	default:
		s.Log.Error(err, "writing request")
		s.fail(w, r, http.StatusInternalServerError, "internal error")
	}
}
