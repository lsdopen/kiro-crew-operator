package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// maxImageLen matches the CRD limit on image references.
const maxImageLen = 512

// ErrConfigNotFound means the operator has not created the `default` config yet.
var ErrConfigNotFound = errors.New("config not found")

// ConfigEdit is the form input of the config editor.
type ConfigEdit struct {
	AllowedSizes                               []string
	DefaultSize                                string
	GatewayImage, TailscaleImage, MCPConfigRef string
	// MCPConfigs is the allowlist crews' MCP config may be chosen from.
	MCPConfigs []string
}

// maxMCPConfigs matches the CRD limit on the allowlist.
const maxMCPConfigs = 64

func (e ConfigEdit) validate() (v1alpha1.KiroCrewConfigSpec, error) {
	var spec v1alpha1.KiroCrewConfigSpec
	for _, name := range e.AllowedSizes {
		size := v1alpha1.Size(name)
		if _, ok := v1alpha1.SizeProfiles[size]; !ok {
			return spec, &InvalidError{msgUnknownSize}
		}
		if !slices.Contains(spec.AllowedSizes, size) {
			spec.AllowedSizes = append(spec.AllowedSizes, size)
		}
	}
	if len(spec.AllowedSizes) == 0 {
		return spec, &InvalidError{"allow at least one size"}
	}
	spec.DefaultSize = v1alpha1.Size(e.DefaultSize)
	if !slices.Contains(spec.AllowedSizes, spec.DefaultSize) {
		return spec, &InvalidError{"the default size must be one of the allowed sizes"}
	}
	var err error
	if spec.Gateway.Image, err = cleanImage("gateway image", e.GatewayImage); err != nil {
		return spec, err
	}
	if spec.Tailscale.Image, err = cleanImage("tailscale image", e.TailscaleImage); err != nil {
		return spec, err
	}
	spec.MCPConfigRef = strings.TrimSpace(e.MCPConfigRef)
	if spec.MCPConfigRef != "" && len(validation.IsDNS1123Subdomain(spec.MCPConfigRef)) > 0 {
		return spec, &InvalidError{"MCP config must be a ConfigMap name"}
	}
	for _, name := range e.MCPConfigs {
		name = strings.TrimSpace(name)
		if name == "" || slices.Contains(spec.MCPConfigs, name) {
			continue
		}
		if len(validation.IsDNS1123Subdomain(name)) > 0 {
			return spec, &InvalidError{"allowed MCP configs must be ConfigMap names"}
		}
		spec.MCPConfigs = append(spec.MCPConfigs, name)
	}
	if len(spec.MCPConfigs) > maxMCPConfigs {
		return spec, &InvalidError{fmt.Sprintf("allow at most %d MCP configs", maxMCPConfigs)}
	}
	return spec, nil
}

func cleanImage(label, v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) > maxImageLen || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", &InvalidError{fmt.Sprintf("invalid %s", label)}
	}
	return v, nil
}

// UpdateConfig replaces the editable fields of the `default` config. Admin only.
func (s *Store) UpdateConfig(ctx context.Context, me *v1alpha1.KiroCrewAccount, in ConfigEdit) error {
	if me.Spec.Role != v1alpha1.RoleAdmin {
		return ErrForbidden
	}
	spec, err := in.validate()
	if err != nil {
		return err
	}
	cfg := &v1alpha1.KiroCrewConfig{}
	err = s.Live.Get(ctx, client.ObjectKey{Name: v1alpha1.ConfigName}, cfg)
	if apierrors.IsNotFound(err) {
		return ErrConfigNotFound
	}
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	orig := cfg.DeepCopy()
	cfg.Spec = spec
	return writeErr("config", s.Client.Patch(ctx, cfg, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})))
}

// configSpec reads the `default` config from the cache; absent means defaults.
func (s *Store) configSpec(ctx context.Context) (v1alpha1.KiroCrewConfigSpec, error) {
	cfg := &v1alpha1.KiroCrewConfig{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: v1alpha1.ConfigName}, cfg)
	if apierrors.IsNotFound(err) {
		return v1alpha1.KiroCrewConfigSpec{}, nil
	}
	if err != nil {
		return v1alpha1.KiroCrewConfigSpec{}, fmt.Errorf("reading config: %w", err)
	}
	return cfg.Spec, nil
}
