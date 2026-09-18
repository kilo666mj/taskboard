package server

import (
	"slices"
	"strings"

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
