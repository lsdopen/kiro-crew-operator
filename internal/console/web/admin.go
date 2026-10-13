package web

import (
	"net/http"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
	"github.com/lsdopen/kiro-crew-operator/internal/console/auth"
	"github.com/lsdopen/kiro-crew-operator/internal/console/authz"
)

// updateAccount reads only role and manager from the form. The store enforces admin.
func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	in := authz.AccountEdit{Role: r.PostForm.Get("role"), Manager: r.PostForm.Get("manager")}
	if err := s.Store.UpdateAccount(r.Context(), acct, r.PathValue("name"), in); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "accounts", "Account updated.")
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	if err := s.Store.DeleteAccount(r.Context(), acct, r.PathValue("name")); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "accounts", "Account deleted.")
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	in := authz.ConfigEdit{
		AllowedSizes: r.PostForm["allowed"], DefaultSize: r.PostForm.Get("default_size"),
		GatewayImage: r.PostForm.Get("gateway_image"), TailscaleImage: r.PostForm.Get("tailscale_image"),
		MCPConfigRef: r.PostForm.Get("mcp_config_ref"),
	}
	if err := s.Store.UpdateConfig(r.Context(), acct, in); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "config", "Config saved.")
}
