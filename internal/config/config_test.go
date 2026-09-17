package config

import "testing"

func TestMCPDefaultTaskType(t *testing.T) {
	t.Setenv("TASKBOARD_MCP_DEFAULT_TYPE", "personal")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPDefaultTaskType != "personal" {
		t.Fatalf("default task type = %q, want personal", cfg.MCPDefaultTaskType)
	}

	t.Setenv("TASKBOARD_MCP_DEFAULT_TYPE", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid MCP default task type was accepted")
	}
}

func TestCloudflareAccessConfiguration(t *testing.T) {
	t.Setenv("TASKBOARD_BROWSER_AUTH_MODE", BrowserAuthCloudflareAccess)
	t.Setenv("TASKBOARD_CF_ACCESS_TEAM_DOMAIN", "https://team.cloudflareaccess.com/")
	t.Setenv("TASKBOARD_CF_ACCESS_AUD", "access-audience")
	t.Setenv("TASKBOARD_CF_ACCESS_ALLOWED_GROUPS", "operators, taskboard-users")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CloudflareAccessEnabled() || cfg.CFAccessTeamDomain != "https://team.cloudflareaccess.com" || len(cfg.CFAccessGroups) != 2 {
		t.Fatalf("Cloudflare Access config = %#v", cfg)
	}
}

func TestCloudflareAccessConfigurationFailsClosed(t *testing.T) {
	for _, teamDomain := range []string{"", "http://team.cloudflareaccess.com", "https://team.cloudflareaccess.com/path"} {
		t.Run(teamDomain, func(t *testing.T) {
			t.Setenv("TASKBOARD_BROWSER_AUTH_MODE", BrowserAuthCloudflareAccess)
			t.Setenv("TASKBOARD_CF_ACCESS_TEAM_DOMAIN", teamDomain)
			t.Setenv("TASKBOARD_CF_ACCESS_AUD", "access-audience")
			if _, err := Load(); err == nil {
				t.Fatalf("invalid team domain %q accepted", teamDomain)
			}
		})
	}
}

func TestPostgresDatabaseURL(t *testing.T) {
	t.Setenv("TASKBOARD_DATABASE_URL", "postgresql://taskboard@db01,db02/taskboard?target_session_attrs=read-write")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL == "" {
		t.Fatal("PostgreSQL database URL was not loaded")
	}

	for _, value := range []string{"sqlite:///tmp/taskboard.db", "postgresql:///taskboard", "postgresql://db01"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TASKBOARD_DATABASE_URL", value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid database URL %q accepted", value)
			}
		})
	}
}
