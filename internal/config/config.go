package config

import (
	"fmt"
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
	ListenAddress        string
	MetricsListenAddress string
	DatabasePath         string
	DatabaseURL          string
	AuthToken            string
	AllowInsecure        bool
	AllowedHosts         []string
	LeaseDuration        time.Duration
	MCPDefaultTaskType   string
	BrowserAuthMode      string
	VAPIDPublicKey       string
	VAPIDPrivateKey      string
	VAPIDContact         string
	OIDCIssuer           string
	OIDCClientID         string
	OIDCClientSecret     string
	OIDCRedirectURL      string
	OIDCAllowedSubjects  []string
	OIDCAllowedEmails    []string
	OIDCAllowedGroups    []string
	CFAccessTeamDomain   string
	CFAccessAudience     string
	CFAccessSubjects     []string
	CFAccessEmails       []string
	CFAccessGroups       []string
	DefaultRole          string
	OwnerGroups          []string
	AdminGroups          []string
	MemberGroups         []string
	ViewerGroups         []string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:        env("TASKBOARD_LISTEN_ADDRESS", "127.0.0.1:8095"),
		MetricsListenAddress: strings.TrimSpace(os.Getenv("TASKBOARD_METRICS_LISTEN_ADDRESS")),
		DatabasePath:         env("TASKBOARD_DATABASE_PATH", "taskboard.db"),
		DatabaseURL:          strings.TrimSpace(os.Getenv("TASKBOARD_DATABASE_URL")),
		AuthToken:            strings.TrimSpace(os.Getenv("TASKBOARD_AUTH_TOKEN")),
		AllowInsecure:        envBool("TASKBOARD_ALLOW_INSECURE", false),
		AllowedHosts:         split(os.Getenv("TASKBOARD_ALLOWED_HOSTS")),
		LeaseDuration:        2 * time.Minute,
		MCPDefaultTaskType:   env("TASKBOARD_MCP_DEFAULT_TYPE", "work"),
		BrowserAuthMode:      env("TASKBOARD_BROWSER_AUTH_MODE", BrowserAuthOIDC),
		VAPIDPublicKey:       strings.TrimSpace(os.Getenv("TASKBOARD_VAPID_PUBLIC_KEY")),
		VAPIDPrivateKey:      strings.TrimSpace(os.Getenv("TASKBOARD_VAPID_PRIVATE_KEY")),
		VAPIDContact:         env("TASKBOARD_VAPID_CONTACT", "mailto:admin@localhost"),
		OIDCIssuer:           strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_ISSUER")),
		OIDCClientID:         strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_CLIENT_ID")),
		OIDCClientSecret:     strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_CLIENT_SECRET")),
		OIDCRedirectURL:      strings.TrimSpace(os.Getenv("TASKBOARD_OIDC_REDIRECT_URL")),
		OIDCAllowedSubjects:  split(os.Getenv("TASKBOARD_OIDC_ALLOWED_SUBJECTS")),
		OIDCAllowedEmails:    split(os.Getenv("TASKBOARD_OIDC_ALLOWED_EMAILS")),
		OIDCAllowedGroups:    split(os.Getenv("TASKBOARD_OIDC_ALLOWED_GROUPS")),
		CFAccessTeamDomain:   strings.TrimRight(strings.TrimSpace(os.Getenv("TASKBOARD_CF_ACCESS_TEAM_DOMAIN")), "/"),
		CFAccessAudience:     strings.TrimSpace(os.Getenv("TASKBOARD_CF_ACCESS_AUD")),
		CFAccessSubjects:     split(os.Getenv("TASKBOARD_CF_ACCESS_ALLOWED_SUBJECTS")),
		CFAccessEmails:       split(os.Getenv("TASKBOARD_CF_ACCESS_ALLOWED_EMAILS")),
		CFAccessGroups:       split(os.Getenv("TASKBOARD_CF_ACCESS_ALLOWED_GROUPS")),
		DefaultRole:          env("TASKBOARD_DEFAULT_ROLE", "admin"),
		OwnerGroups:          split(os.Getenv("TASKBOARD_OWNER_GROUPS")),
		AdminGroups:          split(os.Getenv("TASKBOARD_ADMIN_GROUPS")),
		MemberGroups:         split(os.Getenv("TASKBOARD_MEMBER_GROUPS")),
		ViewerGroups:         split(os.Getenv("TASKBOARD_VIEWER_GROUPS")),
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
	if cfg.AllowInsecure && cfg.AuthToken == "" && len(cfg.AllowedHosts) == 0 {
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
	if cfg.BrowserAuthMode != BrowserAuthOIDC && cfg.BrowserAuthMode != BrowserAuthCloudflareAccess {
		return Config{}, fmt.Errorf("TASKBOARD_BROWSER_AUTH_MODE must be oidc or cloudflare_access")
	}
	switch cfg.DefaultRole {
	case "owner", "admin", "member", "viewer":
	default:
		return Config{}, fmt.Errorf("TASKBOARD_DEFAULT_ROLE must be owner, admin, member, or viewer")
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
	if cfg.BrowserAuthMode == BrowserAuthCloudflareAccess {
		if cfg.CFAccessTeamDomain == "" || cfg.CFAccessAudience == "" {
			return Config{}, fmt.Errorf("TASKBOARD_CF_ACCESS_TEAM_DOMAIN and TASKBOARD_CF_ACCESS_AUD are required in cloudflare_access mode")
		}
		teamDomain, err := url.Parse(cfg.CFAccessTeamDomain)
		if err != nil || teamDomain.Scheme != "https" || teamDomain.Hostname() == "" || teamDomain.User != nil || teamDomain.RawQuery != "" || teamDomain.Fragment != "" || (teamDomain.Path != "" && teamDomain.Path != "/") {
			return Config{}, fmt.Errorf("TASKBOARD_CF_ACCESS_TEAM_DOMAIN must be an HTTPS origin")
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
