package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/service"
)

func TestRoleForGroupsUsesHighestMappedRoleAndSafeDefault(t *testing.T) {
	cfg := config.Config{
		DefaultRole:  "viewer",
		OwnerGroups:  []string{"owners"},
		AdminGroups:  []string{"admins"},
		MemberGroups: []string{"members"},
		ViewerGroups: []string{"viewers"},
	}
	for _, test := range []struct {
		name   string
		groups []string
		want   service.Role
	}{
		{"owner precedence", []string{"viewers", "owners", "members"}, service.RoleOwner},
		{"admin precedence", []string{"viewers", "admins", "members"}, service.RoleAdmin},
		{"member", []string{"members"}, service.RoleMember},
		{"viewer", []string{"viewers"}, service.RoleViewer},
		{"default", []string{"unmapped"}, service.RoleViewer},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := roleForGroups(cfg, test.groups); got != test.want {
				t.Fatalf("role = %q, want %q", got, test.want)
			}
		})
	}
	if got := roleForGroups(config.Config{}, nil); got != service.RoleAdmin {
		t.Fatalf("zero-config role = %q, want backwards-compatible admin", got)
	}
}

func TestCloudflareBrowserGroupsBecomeRoleButMCPRemainsAgent(t *testing.T) {
	_, database, _ := serverFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, true, logger)
	access := &cloudflareAccess{verifier: fakeCloudflareAccessVerifier{identity: cloudflareAccessIdentity{
		Subject: "principal", Email: "person@example.com", Groups: []string{"engineering"},
	}}}
	cfg := config.Config{DefaultRole: "viewer", MemberGroups: []string{"engineering"}}
	handler := auth(cfg, sessions, access, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r.Context())
		w.Header().Set("X-Test-Role", string(p.Role))
		if p.Agent {
			w.Header().Set("X-Test-Agent", "true")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		path      string
		wantRole  string
		wantAgent string
	}{
		{"/api/v1/tasks", "member", ""},
		{"/mcp", "agent", "true"},
	} {
		request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com"+test.path, nil)
		request.Header.Set(cloudflareAccessJWTHeader, "assertion")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent || response.Header().Get("X-Test-Role") != test.wantRole || response.Header().Get("X-Test-Agent") != test.wantAgent {
			t.Fatalf("%s status/role/agent = %d/%q/%q", test.path, response.Code, response.Header().Get("X-Test-Role"), response.Header().Get("X-Test-Agent"))
		}
	}
}

func TestAgentPrincipalUsesPerIdentitySafetyPolicy(t *testing.T) {
	capabilities := []string{service.CapabilityTaskRead, service.CapabilityTaskClaim}
	maxConcurrent := 1
	requireIdempotency := true
	cfg := config.Config{
		AgentCapabilities:        []string{service.CapabilityTaskRead, service.CapabilityTaskCreate},
		AgentMaxConcurrentRuns:   4,
		AgentMaxPickupsPerMinute: 30,
		AgentMaxRunDuration:      8 * time.Hour,
		AgentPolicies: map[string]config.AgentPolicy{
			"agent:build": {Capabilities: &capabilities, MaxConcurrentRuns: &maxConcurrent, RequireIdempotency: &requireIdempotency},
		},
	}
	principal := agentPrincipal(cfg, "agent:build")
	if !principal.HasCapability(service.CapabilityTaskClaim) || principal.HasCapability(service.CapabilityTaskCreate) || principal.Policy.MaxConcurrentRuns != 1 || !principal.Policy.RequireIdempotency {
		t.Fatalf("resolved agent policy = %+v", principal.Policy)
	}
}
