package config

import "testing"

func TestInsecureModeIsLoopbackOnlyWithSafeHostDefaults(t *testing.T) {
	t.Setenv("TASKBOARD_ALLOW_INSECURE", "true")
	for _, listener := range []string{"0.0.0.0:8095", ":8095", "[::]:8095", "192.168.1.20:8095"} {
		t.Setenv("TASKBOARD_LISTEN_ADDRESS", listener)
		if _, err := Load(); err == nil {
			t.Fatalf("insecure mode accepted non-loopback listener %q", listener)
		}
	}

	t.Setenv("TASKBOARD_LISTEN_ADDRESS", "127.0.0.1:8095")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"localhost", "127.0.0.1", "::1"}
	if len(cfg.AllowedHosts) != len(want) {
		t.Fatalf("allowed hosts = %v, want %v", cfg.AllowedHosts, want)
	}
	for index := range want {
		if cfg.AllowedHosts[index] != want[index] {
			t.Fatalf("allowed hosts = %v, want %v", cfg.AllowedHosts, want)
		}
	}

	t.Setenv("TASKBOARD_ALLOWED_HOSTS", "board.localhost")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedHosts) != 1 || cfg.AllowedHosts[0] != "board.localhost" {
		t.Fatalf("explicit allowed hosts = %v", cfg.AllowedHosts)
	}
}

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

func TestMetricsListenerIsOptionalAndValidated(t *testing.T) {
	t.Setenv("TASKBOARD_METRICS_LISTEN_ADDRESS", "127.0.0.1:9090")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsListenAddress != "127.0.0.1:9090" {
		t.Fatalf("metrics address = %q", cfg.MetricsListenAddress)
	}

	t.Setenv("TASKBOARD_METRICS_LISTEN_ADDRESS", "not-an-address")
	if _, err := Load(); err == nil {
		t.Fatal("invalid metrics listener was accepted")
	}
}
