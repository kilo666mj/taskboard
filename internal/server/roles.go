package server

import (
	"slices"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

func roleForGroups(cfg config.Config, groups []string) service.Role {
	for _, mapping := range []struct {
		role   service.Role
		groups []string
	}{
		{service.RoleOwner, cfg.OwnerGroups},
		{service.RoleAdmin, cfg.AdminGroups},
		{service.RoleMember, cfg.MemberGroups},
		{service.RoleViewer, cfg.ViewerGroups},
	} {
		for _, group := range groups {
			if slices.Contains(mapping.groups, strings.TrimSpace(group)) {
				return mapping.role
			}
		}
	}
	role := service.Role(cfg.DefaultRole)
	if !service.IsHumanRole(role) {
		return service.RoleAdmin
	}
	return role
}

func browserPrincipal(cfg config.Config, identity store.BrowserIdentity) service.Principal {
	return service.HumanPrincipalWithRole(identity.Subject, roleForGroups(cfg, identity.Groups))
}

func agentPrincipal(cfg config.Config, id string) service.Principal {
	policy := service.AgentPolicy{
		Capabilities:        capabilitySet(cfg.AgentCapabilities),
		MaxConcurrentRuns:   cfg.AgentMaxConcurrentRuns,
		MaxPickupsPerMinute: cfg.AgentMaxPickupsPerMinute,
		MaxRunDuration:      cfg.AgentMaxRunDuration,
		RequireIdempotency:  cfg.AgentRequireIdempotency,
	}
	if policy.MaxConcurrentRuns == 0 && policy.MaxPickupsPerMinute == 0 && policy.MaxRunDuration == 0 && len(policy.Capabilities) == 0 {
		policy = service.DefaultAgentPolicy()
	}
	if override, ok := cfg.AgentPolicies[id]; ok {
		if override.Capabilities != nil {
			policy.Capabilities = capabilitySet(*override.Capabilities)
		}
		if override.MaxConcurrentRuns != nil {
			policy.MaxConcurrentRuns = *override.MaxConcurrentRuns
		}
		if override.MaxPickupsPerMinute != nil {
			policy.MaxPickupsPerMinute = *override.MaxPickupsPerMinute
		}
		if override.MaxRunSeconds != nil {
			policy.MaxRunDuration = time.Duration(*override.MaxRunSeconds) * time.Second
		}
		if override.RequireIdempotency != nil {
			policy.RequireIdempotency = *override.RequireIdempotency
		}
	}
	return service.AgentPrincipalWithPolicy(id, policy)
}

func capabilitySet(capabilities []string) map[string]bool {
	result := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		result[capability] = true
	}
	return result
}
