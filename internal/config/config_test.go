package config

import (
	"testing"
	"time"
)

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

func TestNonLoopbackListenerRequiresAllowedHosts(t *testing.T) {
	t.Setenv("TASKBOARD_LISTEN_ADDRESS", "0.0.0.0:8095")
	t.Setenv("TASKBOARD_AUTH_TOKEN", "0123456789abcdef0123456789abcdef")
	if _, err := Load(); err == nil {
		t.Fatal("non-loopback listener without allowed hosts was accepted")
	}
	t.Setenv("TASKBOARD_ALLOWED_HOSTS", "taskboard.example.com")
	if _, err := Load(); err != nil {
		t.Fatalf("non-loopback listener with allowed hosts: %v", err)
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

func TestInstanceTaskType(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskType != "" {
		t.Fatalf("default instance task type = %q, want empty", cfg.TaskType)
	}
	t.Setenv("TASKBOARD_MCP_DEFAULT_TYPE", "work")
	t.Setenv("TASKBOARD_TASK_TYPE", " Personal ")
	if cfg, err = Load(); err != nil || cfg.TaskType != "personal" || cfg.MCPDefaultTaskType != "personal" {
		t.Fatalf("instance task type = %q/%q, %v", cfg.TaskType, cfg.MCPDefaultTaskType, err)
	}
	t.Setenv("TASKBOARD_TASK_TYPE", "both")
	if _, err := Load(); err == nil {
		t.Fatal("invalid instance task type was accepted")
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

func TestWorkplaceRoleConfiguration(t *testing.T) {
	t.Setenv("TASKBOARD_DEFAULT_ROLE", "viewer")
	t.Setenv("TASKBOARD_OWNER_GROUPS", "taskboard-owners")
	t.Setenv("TASKBOARD_ADMIN_GROUPS", "taskboard-admins")
	t.Setenv("TASKBOARD_MEMBER_GROUPS", "engineering,operations")
	t.Setenv("TASKBOARD_VIEWER_GROUPS", "auditors")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultRole != "viewer" || len(cfg.MemberGroups) != 2 || cfg.OwnerGroups[0] != "taskboard-owners" {
		t.Fatalf("role configuration = %#v", cfg)
	}

	t.Setenv("TASKBOARD_DEFAULT_ROLE", "superuser")
	if _, err := Load(); err == nil {
		t.Fatal("invalid default role was accepted")
	}
}

func TestWorkplaceRoleDefaultsToMember(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultRole != "member" {
		t.Fatalf("default role = %q, want member", cfg.DefaultRole)
	}
}

func TestIdentityProviderRequiresAllowlistOrExplicitPolicyTrust(t *testing.T) {
	t.Run("OIDC", func(t *testing.T) {
		t.Setenv("TASKBOARD_OIDC_ISSUER", "https://idp.example.com")
		t.Setenv("TASKBOARD_OIDC_CLIENT_ID", "taskboard")
		t.Setenv("TASKBOARD_OIDC_REDIRECT_URL", "https://taskboard.example.com/api/v1/auth/oidc/callback")
		if _, err := Load(); err == nil {
			t.Fatal("OIDC without an application allow-list or explicit policy trust was accepted")
		}
		t.Setenv("TASKBOARD_OIDC_TRUST_PROVIDER_POLICY", "true")
		if _, err := Load(); err != nil {
			t.Fatalf("OIDC with explicit provider-policy trust: %v", err)
		}
	})

	t.Run("Cloudflare Access", func(t *testing.T) {
		t.Setenv("TASKBOARD_BROWSER_AUTH_MODE", BrowserAuthCloudflareAccess)
		t.Setenv("TASKBOARD_CF_ACCESS_TEAM_DOMAIN", "https://team.cloudflareaccess.com")
		t.Setenv("TASKBOARD_CF_ACCESS_AUD", "access-audience")
		if _, err := Load(); err == nil {
			t.Fatal("Cloudflare Access without an application allow-list or explicit policy trust was accepted")
		}
		t.Setenv("TASKBOARD_CF_ACCESS_TRUST_POLICY", "true")
		if _, err := Load(); err != nil {
			t.Fatalf("Cloudflare Access with explicit policy trust: %v", err)
		}
	})
}

func TestAgentSafetyPolicyConfiguration(t *testing.T) {
	t.Setenv("TASKBOARD_AGENT_CAPABILITIES", "task:read,task:claim,task:update")
	t.Setenv("TASKBOARD_AGENT_MAX_CONCURRENT_RUNS", "2")
	t.Setenv("TASKBOARD_AGENT_MAX_PICKUPS_PER_MINUTE", "12")
	t.Setenv("TASKBOARD_AGENT_MAX_RUN_SECONDS", "3600")
	t.Setenv("TASKBOARD_AGENT_REQUIRE_IDEMPOTENCY", "true")
	t.Setenv("TASKBOARD_AGENT_POLICIES_JSON", `{"cloudflare_access:service_token:build":{"capabilities":["task:read","task:claim"],"max_concurrent_runs":1,"require_idempotency":true}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.AgentPolicies["cloudflare_access:service_token:build"]
	if cfg.AgentMaxConcurrentRuns != 2 || cfg.AgentMaxPickupsPerMinute != 12 || cfg.AgentMaxRunDuration != time.Hour || !cfg.AgentRequireIdempotency || policy.Capabilities == nil || len(*policy.Capabilities) != 2 {
		t.Fatalf("agent policy configuration = %#v / %#v", cfg, policy)
	}

	t.Setenv("TASKBOARD_AGENT_CAPABILITIES", "task:read,unknown:power")
	if _, err := Load(); err == nil {
		t.Fatal("unknown agent capability was accepted")
	}
}

func TestMCPHumanDelegationConfiguration(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPHumanDelegation {
		t.Fatal("MCP human delegation is enabled by default")
	}
	t.Setenv("TASKBOARD_MCP_HUMAN_DELEGATION", "true")
	if cfg, err = Load(); err != nil || !cfg.MCPHumanDelegation {
		t.Fatalf("enabled delegation = %v, %v", cfg.MCPHumanDelegation, err)
	}
	t.Setenv("TASKBOARD_MCP_HUMAN_DELEGATION", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("invalid MCP human delegation value was accepted")
	}
}

func TestAdministrativeLifecycleConfiguration(t *testing.T) {
	t.Setenv("TASKBOARD_RETENTION_DAYS", "90")
	t.Setenv("TASKBOARD_WEBHOOK_URL", "https://hooks.example.com/taskboard")
	t.Setenv("TASKBOARD_WEBHOOK_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("TASKBOARD_WEBHOOK_MAX_ATTEMPTS", "12")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionDays != 90 || cfg.WebhookMaxAttempts != 12 || cfg.WebhookURL == "" {
		t.Fatalf("administrative configuration = %#v", cfg)
	}

	t.Setenv("TASKBOARD_WEBHOOK_URL", "http://internal.example.com/hook")
	if _, err := Load(); err == nil {
		t.Fatal("non-HTTPS webhook URL was accepted")
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
