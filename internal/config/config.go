package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	pwakit "github.com/kilo666mj/pwa-kit"
)

const (
	BrowserAuthOIDC             = "oidc"
	BrowserAuthCloudflareAccess = "cloudflare_access"
)

type Config struct {
	ListenAddress            string
	MetricsListenAddress     string
	DatabasePath             string
	DatabaseURL              string
	AuthToken                string
	AllowInsecure            bool
	AllowedHosts             []string
	LeaseDuration            time.Duration
	MCPDefaultTaskType       string
	TaskType                 string
	BrowserAuthMode          string
	VAPIDPublicKey           string
	VAPIDPrivateKey          string
	VAPIDContact             string
	OIDCIssuer               string
	OIDCClientID             string
	OIDCClientSecret         string
	OIDCRedirectURL          string
	OIDCAllowedSubjects      []string
	OIDCAllowedEmails        []string
	OIDCAllowedGroups        []string
	OIDCTrustProviderPolicy  bool
	CFAccessTeamDomain       string
	CFAccessAudience         string
	CFAccessSubjects         []string
	CFAccessEmails           []string
	CFAccessGroups           []string
	CFAccessTrustPolicy      bool
	DefaultRole              string
	OwnerGroups              []string
	AdminGroups              []string
	MemberGroups             []string
	ViewerGroups             []string
	AgentCapabilities        []string
	AgentMaxConcurrentRuns   int
	AgentMaxPickupsPerMinute int
	AgentMaxRunDuration      time.Duration
	AgentRequireIdempotency  bool
	MCPHumanDelegation       bool
	AgentPolicies            map[string]AgentPolicy
	RetentionDays            int
	WebhookURL               string
	WebhookSecret            string
	WebhookMaxAttempts       int
}

type AgentPolicy struct {
	Capabilities        *[]string `json:"capabilities,omitempty"`
	MaxConcurrentRuns   *int      `json:"max_concurrent_runs,omitempty"`
	MaxPickupsPerMinute *int      `json:"max_pickups_per_minute,omitempty"`
	MaxRunSeconds       *int      `json:"max_run_seconds,omitempty"`
	RequireIdempotency  *bool     `json:"require_idempotency,omitempty"`
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:            env("TASKBOARD_LISTEN_ADDRESS", "127.0.0.1:8095"),
		MetricsListenAddress:     strings.TrimSpace(os.Getenv("TASKBOARD_METRICS_LISTEN_ADDRESS")),
		DatabasePath:             env("TASKBOARD_DATABASE_PATH", "taskboard.db"),
		DatabaseURL:              strings.TrimSpace(os.Getenv("TASKBOARD_DATABASE_URL")),
		AuthToken:                strings.TrimSpace(os.Getenv("TASKBOARD_AUTH_TOKEN")),
		AllowInsecure:            envBool("TASKBOARD_ALLOW_INSECURE", false),
		AllowedHosts:             split(os.Getenv("TASKBOARD_ALLOWED_HOSTS")),
		LeaseDuration:            2 * time.Minute,
		MCPDefaultTaskType:       env("TASKBOARD_MCP_DEFAULT_TYPE", "work"),
		TaskType:                 strings.ToLower(strings.TrimSpace(os.Getenv("TASKBOARD_TASK_TYPE"))),
		BrowserAuthMode:          env("TASKBOARD_BROWSER_AUTH_MODE", BrowserAuthOIDC),
		VAPIDPublicKey:           strings.TrimSpace(os.Getenv("TASKBOARD_VAPID_PUBLIC_KEY")),
		VAPIDPrivateKey:          strings.TrimSpace(os.Getenv("TASKBOARD_VAPID_PRIVATE_KEY")),
		VAPIDContact:             env("TASKBOARD_VAPID_CONTACT", "mailto:admin@localhost"),
		OIDCIssuer:               strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_ISSUER")),
		OIDCClientID:             strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_CLIENT_ID")),
		OIDCClientSecret:         strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_CLIENT_SECRET")),
		OIDCRedirectURL:          strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_REDIRECT_URL")),
		OIDCAllowedSubjects:      split(os.Getenv("TASKBOARD_OIDC_ALLOWED_SUBJECTS")),
		OIDCAllowedEmails:        split(os.Getenv("TASKBOARD_OIDC_ALLOWED_EMAILS")),
		OIDCAllowedGroups:        split(os.Getenv("TASKBOARD_OIDC_ALLOWED_GROUPS")),
		OIDCTrustProviderPolicy:  envBool("TASKBOARD_OIDC_TRUST_PROVIDER_POLICY", false),
		CFAccessTeamDomain:       strings.TrimRight(strings.TrimSpace(os.Getenv("TASKBOARD_CF_ACCESS_TEAM_DOMAIN")), "/"),
		CFAccessAudience:         strings.TrimSpace(os.Getenv("TASKBOARD_CF_ACCESS_AUD")),
		CFAccessSubjects:         split(os.Getenv("TASKBOARD_CF_ACCESS_ALLOWED_SUBJECTS")),
		CFAccessEmails:           split(os.Getenv("TASKBOARD_CF_ACCESS_ALLOWED_EMAILS")),
		CFAccessGroups:           split(os.Getenv("TASKBOARD_CF_ACCESS_ALLOWED_GROUPS")),
		CFAccessTrustPolicy:      envBool("TASKBOARD_CF_ACCESS_TRUST_POLICY", false),
		DefaultRole:              env("TASKBOARD_DEFAULT_ROLE", "member"),
		OwnerGroups:              split(os.Getenv("TASKBOARD_OWNER_GROUPS")),
		AdminGroups:              split(os.Getenv("TASKBOARD_ADMIN_GROUPS")),
		MemberGroups:             split(os.Getenv("TASKBOARD_MEMBER_GROUPS")),
		ViewerGroups:             split(os.Getenv("TASKBOARD_VIEWER_GROUPS")),
		AgentCapabilities:        split(env("TASKBOARD_AGENT_CAPABILITIES", "task:read,task:create,task:claim,task:update,task:message,task:escalate,task:control,task:reference,task:handoff,task:evidence,task:session,task:usage,task:complete,worker:advertise,template:read,template:manage")),
		AgentMaxConcurrentRuns:   envInt("TASKBOARD_AGENT_MAX_CONCURRENT_RUNS", 8),
		AgentMaxPickupsPerMinute: envInt("TASKBOARD_AGENT_MAX_PICKUPS_PER_MINUTE", 30),
		AgentMaxRunDuration:      time.Duration(envInt("TASKBOARD_AGENT_MAX_RUN_SECONDS", 28800)) * time.Second,
		AgentRequireIdempotency:  envBool("TASKBOARD_AGENT_REQUIRE_IDEMPOTENCY", false),
		MCPHumanDelegation:       envBool("TASKBOARD_MCP_HUMAN_DELEGATION", false),
		AgentPolicies:            map[string]AgentPolicy{},
		RetentionDays:            envInt("TASKBOARD_RETENTION_DAYS", 0),
		WebhookURL:               strings.TrimSpace(os.Getenv("TASKBOARD_WEBHOOK_URL")),
		WebhookSecret:            strings.TrimSpace(os.Getenv("TASKBOARD_WEBHOOK_SECRET")),
		WebhookMaxAttempts:       envInt("TASKBOARD_WEBHOOK_MAX_ATTEMPTS", 8),
	}
	if raw := strings.TrimSpace(os.Getenv("TASKBOARD_AGENT_POLICIES_JSON")); raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg.AgentPolicies); err != nil {
			return Config{}, fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON must be a JSON object: %w", err)
		}
		if cfg.AgentPolicies == nil {
			return Config{}, fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON must be a JSON object")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return Config{}, fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON must contain one JSON object")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("TASKBOARD_AGENT_REQUIRE_IDEMPOTENCY")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("TASKBOARD_AGENT_REQUIRE_IDEMPOTENCY must be true or false")
		}
		cfg.AgentRequireIdempotency = value
	}
	if raw := strings.TrimSpace(os.Getenv("TASKBOARD_MCP_HUMAN_DELEGATION")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("TASKBOARD_MCP_HUMAN_DELEGATION must be true or false")
		}
		cfg.MCPHumanDelegation = value
	}
	if value := strings.TrimSpace(os.Getenv("TASKBOARD_LEASE_SECONDS")); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 30 || seconds > 3600 {
			return Config{}, fmt.Errorf("TASKBOARD_LEASE_SECONDS must be between 30 and 3600")
		}
		cfg.LeaseDuration = time.Duration(seconds) * time.Second
	}
	host, _, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return Config{}, fmt.Errorf("invalid TASKBOARD_LISTEN_ADDRESS: %w", err)
	}
	if cfg.MetricsListenAddress != "" {
		if _, _, err := net.SplitHostPort(cfg.MetricsListenAddress); err != nil {
			return Config{}, fmt.Errorf("invalid TASKBOARD_METRICS_LISTEN_ADDRESS: %w", err)
		}
	}
	if cfg.AllowInsecure && cfg.AuthToken == "" && !isLoopback(host) {
		return Config{}, fmt.Errorf("TASKBOARD_ALLOW_INSECURE without a token requires a loopback listener")
	}
	if !cfg.AllowInsecure && cfg.AuthToken == "" && !isLoopback(host) {
		return Config{}, fmt.Errorf("TASKBOARD_AUTH_TOKEN is required for a non-loopback listener")
	}
	if !isLoopback(host) && len(cfg.AllowedHosts) == 0 {
		return Config{}, fmt.Errorf("TASKBOARD_ALLOWED_HOSTS is required for a non-loopback listener")
	}
	if isLoopback(host) && len(cfg.AllowedHosts) == 0 {
		cfg.AllowedHosts = []string{"localhost", "127.0.0.1", "::1"}
	}
	if cfg.AuthToken != "" && len(cfg.AuthToken) < 32 {
		return Config{}, fmt.Errorf("TASKBOARD_AUTH_TOKEN must contain at least 32 characters")
	}
	if cfg.DatabaseURL != "" {
		databaseURL, err := url.Parse(cfg.DatabaseURL)
		if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Host == "" || databaseURL.Path == "" || databaseURL.Path == "/" {
			return Config{}, fmt.Errorf("TASKBOARD_DATABASE_URL must be a PostgreSQL URL with a host and database name")
		}
	}
	if cfg.MCPDefaultTaskType != "personal" && cfg.MCPDefaultTaskType != "work" {
		return Config{}, fmt.Errorf("TASKBOARD_MCP_DEFAULT_TYPE must be personal or work")
	}
	if cfg.TaskType != "" && cfg.TaskType != "personal" && cfg.TaskType != "work" {
		return Config{}, fmt.Errorf("TASKBOARD_TASK_TYPE must be personal, work, or empty")
	}
	if cfg.TaskType != "" {
		cfg.MCPDefaultTaskType = cfg.TaskType
	}
	if cfg.BrowserAuthMode != BrowserAuthOIDC && cfg.BrowserAuthMode != BrowserAuthCloudflareAccess {
		return Config{}, fmt.Errorf("TASKBOARD_BROWSER_AUTH_MODE must be oidc or cloudflare_access")
	}
	switch cfg.DefaultRole {
	case "owner", "admin", "member", "viewer":
	default:
		return Config{}, fmt.Errorf("TASKBOARD_DEFAULT_ROLE must be owner, admin, member, or viewer")
	}
	if err := validateAgentPolicies(cfg); err != nil {
		return Config{}, err
	}
	if cfg.RetentionDays < 0 || cfg.RetentionDays > 36500 {
		return Config{}, fmt.Errorf("TASKBOARD_RETENTION_DAYS must be between 0 and 36500")
	}
	if cfg.WebhookMaxAttempts < 1 || cfg.WebhookMaxAttempts > 100 {
		return Config{}, fmt.Errorf("TASKBOARD_WEBHOOK_MAX_ATTEMPTS must be between 1 and 100")
	}
	if (cfg.WebhookURL == "") != (cfg.WebhookSecret == "") {
		return Config{}, fmt.Errorf("TASKBOARD_WEBHOOK_URL and TASKBOARD_WEBHOOK_SECRET must be configured together")
	}
	if cfg.WebhookURL != "" {
		webhookURL, err := url.Parse(cfg.WebhookURL)
		if err != nil || webhookURL.Scheme != "https" || webhookURL.Host == "" || webhookURL.User != nil {
			return Config{}, fmt.Errorf("TASKBOARD_WEBHOOK_URL must be an HTTPS URL without user info")
		}
		if len(cfg.WebhookSecret) < 32 {
			return Config{}, fmt.Errorf("TASKBOARD_WEBHOOK_SECRET must contain at least 32 characters")
		}
	}
	if (cfg.VAPIDPublicKey == "") != (cfg.VAPIDPrivateKey == "") {
		return Config{}, fmt.Errorf("TASKBOARD_VAPID_PUBLIC_KEY and TASKBOARD_VAPID_PRIVATE_KEY must be configured together")
	}
	if cfg.VAPIDPublicKey != "" {
		if err := (pwakit.Config{PublicKey: cfg.VAPIDPublicKey, PrivateKey: cfg.VAPIDPrivateKey, Contact: cfg.VAPIDContact}).Validate(); err != nil {
			return Config{}, err
		}
	}

	oidcConfigured := 0
	for _, value := range []string{cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCRedirectURL} {
		if value != "" {
			oidcConfigured++
		}
	}
	if oidcConfigured != 0 && oidcConfigured != 3 {
		return Config{}, fmt.Errorf("TASKBOARD_OIDC_ISSUER, TASKBOARD_OIDC_CLIENT_ID, and TASKBOARD_OIDC_REDIRECT_URL must be configured together")
	}
	if cfg.OIDCEnabled() && len(cfg.OIDCAllowedSubjects) == 0 && len(cfg.OIDCAllowedEmails) == 0 && len(cfg.OIDCAllowedGroups) == 0 && !cfg.OIDCTrustProviderPolicy {
		return Config{}, fmt.Errorf("OIDC requires an application allow-list or TASKBOARD_OIDC_TRUST_PROVIDER_POLICY=true")
	}
	if cfg.BrowserAuthMode == BrowserAuthCloudflareAccess {
		if cfg.CFAccessTeamDomain == "" || cfg.CFAccessAudience == "" {
			return Config{}, fmt.Errorf("TASKBOARD_CF_ACCESS_TEAM_DOMAIN and TASKBOARD_CF_ACCESS_AUD are required in cloudflare_access mode")
		}
		teamDomain, err := url.Parse(cfg.CFAccessTeamDomain)
		if err != nil || teamDomain.Scheme != "https" || teamDomain.Hostname() == "" || teamDomain.User != nil || teamDomain.RawQuery != "" || teamDomain.Fragment != "" || (teamDomain.Path != "" && teamDomain.Path != "/") {
			return Config{}, fmt.Errorf("TASKBOARD_CF_ACCESS_TEAM_DOMAIN must be an HTTPS origin")
		}
		if len(cfg.CFAccessSubjects) == 0 && len(cfg.CFAccessEmails) == 0 && len(cfg.CFAccessGroups) == 0 && !cfg.CFAccessTrustPolicy {
			return Config{}, fmt.Errorf("cloudflare Access requires an application allow-list or TASKBOARD_CF_ACCESS_TRUST_POLICY=true")
		}
	}
	return cfg, nil
}

func (c Config) OIDCEnabled() bool {
	return (c.BrowserAuthMode == "" || c.BrowserAuthMode == BrowserAuthOIDC) && c.OIDCIssuer != "" && c.OIDCClientID != "" && c.OIDCRedirectURL != ""
}

func (c Config) CloudflareAccessEnabled() bool {
	return c.BrowserAuthMode == BrowserAuthCloudflareAccess && c.CFAccessTeamDomain != "" && c.CFAccessAudience != ""
}

func (c Config) BrowserAuthEnabled() bool {
	return c.OIDCEnabled() || c.CloudflareAccessEnabled()
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return parsed
}

func validateAgentPolicies(cfg Config) error {
	known := map[string]bool{
		"task:read": true, "task:create": true, "task:claim": true, "task:update": true,
		"task:message": true, "task:escalate": true, "task:control": true, "task:reference": true, "task:handoff": true, "task:evidence": true, "task:session": true, "task:usage": true, "task:complete": true, "task:sensitive": true, "worker:advertise": true, "template:read": true, "template:manage": true,
	}
	validateCapabilities := func(name string, capabilities []string) error {
		for _, capability := range capabilities {
			if !known[capability] {
				return fmt.Errorf("%s contains unknown capability %q", name, capability)
			}
		}
		return nil
	}
	if err := validateCapabilities("TASKBOARD_AGENT_CAPABILITIES", cfg.AgentCapabilities); err != nil {
		return err
	}
	if cfg.AgentMaxConcurrentRuns < 1 || cfg.AgentMaxConcurrentRuns > 100 {
		return fmt.Errorf("TASKBOARD_AGENT_MAX_CONCURRENT_RUNS must be between 1 and 100")
	}
	if cfg.AgentMaxPickupsPerMinute < 1 || cfg.AgentMaxPickupsPerMinute > 1000 {
		return fmt.Errorf("TASKBOARD_AGENT_MAX_PICKUPS_PER_MINUTE must be between 1 and 1000")
	}
	if cfg.AgentMaxRunDuration < time.Minute || cfg.AgentMaxRunDuration > 7*24*time.Hour {
		return fmt.Errorf("TASKBOARD_AGENT_MAX_RUN_SECONDS must be between 60 and 604800")
	}
	for principal, policy := range cfg.AgentPolicies {
		if strings.TrimSpace(principal) != principal || principal == "" || len(principal) > 200 {
			return fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON contains an invalid principal")
		}
		if policy.Capabilities != nil {
			if err := validateCapabilities("TASKBOARD_AGENT_POLICIES_JSON", *policy.Capabilities); err != nil {
				return err
			}
		}
		if policy.MaxConcurrentRuns != nil && (*policy.MaxConcurrentRuns < 1 || *policy.MaxConcurrentRuns > 100) {
			return fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON max_concurrent_runs must be between 1 and 100")
		}
		if policy.MaxPickupsPerMinute != nil && (*policy.MaxPickupsPerMinute < 1 || *policy.MaxPickupsPerMinute > 1000) {
			return fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON max_pickups_per_minute must be between 1 and 1000")
		}
		if policy.MaxRunSeconds != nil && (*policy.MaxRunSeconds < 60 || *policy.MaxRunSeconds > 604800) {
			return fmt.Errorf("TASKBOARD_AGENT_POLICIES_JSON max_run_seconds must be between 60 and 604800")
		}
	}
	return nil
}

func split(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func isLoopback(host string) bool {
	address := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || address != nil && address.IsLoopback()
}
