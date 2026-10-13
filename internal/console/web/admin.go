package web

import (
	"net/http"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

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
	active := tabAccounts
	if r.PathValue("name") == acct.Name {
		// Render from the fresh account: an admin who demoted themselves must not see admin views.
		fresh := &v1alpha1.KiroCrewAccount{}
		if err := s.Store.Live.Get(r.Context(), client.ObjectKey{Name: acct.Name}, fresh); err != nil {
			s.writeFailed(w, r, err)
			return
		}
		acct = fresh
		if acct.Spec.Role != v1alpha1.RoleAdmin {
			active = "crews"
		}
	}
	s.done(w, r, sess, acct, active, "Account updated.")
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	if err := s.Store.DeleteAccount(r.Context(), acct, r.PathValue("name")); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, tabAccounts, "Account deleted.")
}

func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request, sess *auth.Session, acct *v1alpha1.KiroCrewAccount) {
	in := authz.ConfigEdit{
		AllowedSizes: r.PostForm["allowed"], DefaultSize: r.PostForm.Get("default_size"),
		GatewayImage: r.PostForm.Get("gateway_image"),
		MCPConfigRef: r.PostForm.Get("mcp_config_ref"),
		MCPConfigs:   strings.Fields(r.PostForm.Get("mcp_configs")),
	}
	if err := s.Store.UpdateConfig(r.Context(), acct, in); err != nil {
		s.writeFailed(w, r, err)
		return
	}
	s.done(w, r, sess, acct, "config", "Config saved.")
}
