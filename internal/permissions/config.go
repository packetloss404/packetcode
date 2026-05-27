package permissions

import (
	"github.com/packetcode/packetcode/internal/config"
)

func FromConfig(cfg *config.Config) (*Policy, error) {
	return FromConfigWithProfile(cfg, "")
}

func FromConfigWithProfile(cfg *config.Config, override Profile) (*Policy, error) {
	pc := config.PermissionConfig{}
	if cfg != nil {
		pc = cfg.Permissions
	}
	if override != "" {
		pc.Profile = string(NormalizeProfile(override))
	} else if cfg != nil && cfg.Behavior.TrustMode {
		pc.Profile = string(ProfileFull)
	}
	return New(pc)
}
