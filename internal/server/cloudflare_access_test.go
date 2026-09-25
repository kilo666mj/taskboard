package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/service"
)

type fakeCloudflareAccessVerifier struct {
	identity cloudflareAccessIdentity
	err      error
}

func (f fakeCloudflareAccessVerifier) Verify(context.Context, string) (cloudflareAccessIdentity, error) {
	return f.identity, f.err
}

func TestCloudflareAccessIdentityAndAllowlists(t *testing.T) {
	access := &cloudflareAccess{
		verifier: fakeCloudflareAccessVerifier{identity: cloudflareAccessIdentity{
			Subject: "person-subject", Email: "person@example.com", Groups: []string{"operators"},
		}},
		allowedGroups: []string{"operators"},
	}
	request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/tasks", nil)
	request.Header.Set(cloudflareAccessJWTHeader, "signed-assertion")
	identity, err := access.identity(request)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "cloudflare_access:person-subject" || identity.Email != "person@example.com" {
		t.Fatalf("identity = %#v", identity)
	}

	access.allowedGroups = []string{"other-group"}
	if _, err := access.identity(request); !errors.Is(err, errInvalidCloudflareAccess) {
		t.Fatalf("disallowed identity error = %v", err)
	}
	request.Header.Add(cloudflareAccessJWTHeader, "second-assertion")
	if _, err := access.identity(request); !errors.Is(err, errInvalidCloudflareAccess) {
		t.Fatalf("multiple assertion error = %v", err)
	}
}

func TestCloudflareAccessAuthenticatesBrowserAndMCP(t *testing.T) {
	_, database, _ := serverFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, true, logger)
	access := &cloudflareAccess{verifier: fakeCloudflareAccessVerifier{identity: cloudflareAccessIdentity{
		Subject: "agent-subject", Email: "agent@example.com",
	}}}
	cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), LeaseDuration: time.Minute}
	handler := auth(cfg, sessions, access, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Actor", actor(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	}))

	browserRequest := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/tasks", nil)
	browserRequest.Header.Set(cloudflareAccessJWTHeader, "browser-assertion")
	browserResponse := httptest.NewRecorder()
	handler.ServeHTTP(browserResponse, browserRequest)
	if browserResponse.Code != http.StatusNoContent || browserResponse.Header().Get("X-Test-Actor") != "cloudflare_access:agent-subject" {
		t.Fatalf("browser status/actor = %d/%q", browserResponse.Code, browserResponse.Header().Get("X-Test-Actor"))
	}

	mcpRequest := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
	mcpRequest.Header.Set(cloudflareAccessJWTHeader, "agent-assertion")
	mcpResponse := httptest.NewRecorder()
	handler.ServeHTTP(mcpResponse, mcpRequest)
	if mcpResponse.Code != http.StatusNoContent || mcpResponse.Header().Get("X-Test-Actor") != "cloudflare_access:agent-subject" {
		t.Fatalf("MCP status/actor = %d/%q", mcpResponse.Code, mcpResponse.Header().Get("X-Test-Actor"))
	}

	ambiguous := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
	ambiguous.Header.Set(cloudflareAccessJWTHeader, "agent-assertion")
	ambiguous.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	ambiguousResponse := httptest.NewRecorder()
	handler.ServeHTTP(ambiguousResponse, ambiguous)
	if ambiguousResponse.Code != http.StatusUnauthorized {
		t.Fatalf("ambiguous credential status = %d, want 401", ambiguousResponse.Code)
	}
}

func TestCloudflareAccessRejectsUnsafeClaims(t *testing.T) {
	for _, identity := range []cloudflareAccessIdentity{
		{Subject: "", Email: "person@example.com"},
		{Subject: "subject", Email: ""},
		{Subject: "subject\nspoof", Email: "person@example.com"},
	} {
		if safeAccessClaim(identity.Subject) && safeAccessClaim(identity.Email) {
			t.Fatalf("unsafe identity accepted: %#v", identity)
		}
	}
}

func TestCloudflareAccessPrincipalSupportsPeopleAndServiceTokens(t *testing.T) {
	person, serviceIdentity, err := cloudflareAccessPrincipal("person-subject", "person@example.com", "")
	if err != nil || person != "person-subject" || serviceIdentity {
		t.Fatalf("person principal = %q/%v, %v", person, serviceIdentity, err)
	}
	service, serviceIdentity, err := cloudflareAccessPrincipal("", "", "service-client.access")
	if err != nil || service != "service_token:service-client.access" || !serviceIdentity {
		t.Fatalf("service principal = %q/%v, %v", service, serviceIdentity, err)
	}
	for _, claims := range [][3]string{
		{"", "", ""},
		{"", "person@example.com", "service-client.access"},
		{"person-subject", "", ""},
		{"person-subject", "person@example.com\nspoof", ""},
	} {
		if principal, _, err := cloudflareAccessPrincipal(claims[0], claims[1], claims[2]); err == nil {
			t.Fatalf("unsafe claims accepted as %q: %#v", principal, claims)
		}
	}
}

func TestCloudflareServiceTokenCannotAuthenticateHumanEndpoint(t *testing.T) {
	_, database, _ := serverFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, true, logger)
	access := &cloudflareAccess{verifier: fakeCloudflareAccessVerifier{identity: cloudflareAccessIdentity{
		Subject: "service_token:build.access", Service: true,
	}}}
	cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), DefaultRole: "admin", LeaseDuration: time.Minute}
	handler := auth(cfg, sessions, access, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal(r.Context()).Agent {
			w.Header().Set("X-Test-Agent", "true")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	browserRequest := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/admin/export", nil)
	browserRequest.Header.Set(cloudflareAccessJWTHeader, "service-assertion")
	browserResponse := httptest.NewRecorder()
	handler.ServeHTTP(browserResponse, browserRequest)
	if browserResponse.Code != http.StatusUnauthorized {
		t.Fatalf("service token browser status = %d, want 401", browserResponse.Code)
	}

	mcpRequest := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
	mcpRequest.Header.Set(cloudflareAccessJWTHeader, "service-assertion")
	mcpResponse := httptest.NewRecorder()
	handler.ServeHTTP(mcpResponse, mcpRequest)
	if mcpResponse.Code != http.StatusNoContent || mcpResponse.Header().Get("X-Test-Agent") != "true" {
		t.Fatalf("service token MCP status/agent = %d/%q", mcpResponse.Code, mcpResponse.Header().Get("X-Test-Agent"))
	}
}

var _ cloudflareAccessVerifier = fakeCloudflareAccessVerifier{}

func TestCloudflareAccessMCPHumanDelegation(t *testing.T) {
	_, database, _ := serverFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, true, logger)
	for _, test := range []struct {
		name       string
		enabled    bool
		identity   cloudflareAccessIdentity
		wantPerson string
	}{
		{name: "disabled", identity: cloudflareAccessIdentity{Subject: "person", Email: "person@example.com"}},
		{name: "person", enabled: true, identity: cloudflareAccessIdentity{Subject: "person", Email: "person@example.com", Groups: []string{"members"}}, wantPerson: "cloudflare_access:person"},
		{name: "service token", enabled: true, identity: cloudflareAccessIdentity{Subject: "service_token:build.access", Service: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			access := &cloudflareAccess{verifier: fakeCloudflareAccessVerifier{identity: test.identity}}
			cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), DefaultRole: "viewer", MemberGroups: []string{"members"}, LeaseDuration: time.Minute, MCPHumanDelegation: test.enabled}
			var got service.Principal
			handler := auth(cfg, sessions, access, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = mcpPrincipal(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
			request.Header.Set(cloudflareAccessJWTHeader, "assertion")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || !got.Agent {
				t.Fatalf("status/principal = %d/%+v", response.Code, got)
			}
			if test.wantPerson == "" {
				if got.OnBehalfOf != nil {
					t.Fatalf("unexpected delegation: %+v", got.OnBehalfOf)
				}
				return
			}
			if got.OnBehalfOf == nil || got.OnBehalfOf.ID != test.wantPerson || got.OnBehalfOf.Agent || got.OnBehalfOf.Role != service.RoleMember {
				t.Fatalf("delegated person = %+v", got.OnBehalfOf)
			}
		})
	}
}

func TestSwitchboardForwardedAccessSubjectIsTrustedOnlyFromDelegationPrincipals(t *testing.T) {
	_, database, logger := serverFixture(t)
	sessions := newBrowserSessions(database, true, logger)
	_, trusted, err := database.CreateAgentCredential(t.Context(), "switchboard", "agent:switchboard", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := database.CreateAgentCredential(t.Context(), "worker", "agent:worker", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.OffboardPrincipal(t.Context(), "cloudflare_access:departed", "test", "left"); err != nil {
		t.Fatal(err)
	}
	shared := strings.Repeat("test-token-", 4)
	base := config.Config{AuthToken: shared, DefaultRole: "member", LeaseDuration: time.Minute, BrowserAuthMode: config.BrowserAuthCloudflareAccess, MCPHumanDelegation: true, MCPDelegationPrincipals: []string{"agent:switchboard"}}
	for _, test := range []struct {
		name, token, subject string
		disabled             bool
		wantStatus           int
		wantPerson           string
	}{
		{name: "trusted", token: trusted, subject: "cloudflare_access:person-1", wantStatus: http.StatusNoContent, wantPerson: "cloudflare_access:person-1"},
		{name: "no header", token: trusted, wantStatus: http.StatusNoContent},
		{name: "delegation disabled", token: trusted, subject: "cloudflare_access:person-1", disabled: true, wantStatus: http.StatusNoContent},
		{name: "other credential", token: other, subject: "cloudflare_access:person-1", wantStatus: http.StatusNoContent},
		{name: "shared bearer", token: shared, subject: "cloudflare_access:person-1", wantStatus: http.StatusNoContent},
		{name: "service token subject", token: trusted, subject: "cloudflare_access:service_token:build.access", wantStatus: http.StatusUnauthorized},
		{name: "unprefixed subject", token: trusted, subject: "person-1", wantStatus: http.StatusUnauthorized},
		{name: "revoked person", token: trusted, subject: "cloudflare_access:departed", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			cfg.MCPHumanDelegation = !test.disabled
			var got service.Principal
			handler := auth(cfg, sessions, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = mcpPrincipal(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
			request.Header.Set("Authorization", "Bearer "+test.token)
			if test.subject != "" {
				request.Header.Set(switchboardAccessSubjectHeader, test.subject)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus != http.StatusNoContent {
				return
			}
			if !got.Agent {
				t.Fatalf("principal = %+v, want agent", got)
			}
			if test.wantPerson == "" {
				if got.OnBehalfOf != nil {
					t.Fatalf("unexpected delegation: %+v", got.OnBehalfOf)
				}
				return
			}
			if got.ID != "agent:switchboard" || got.OnBehalfOf == nil || got.OnBehalfOf.ID != test.wantPerson || got.OnBehalfOf.Role != service.RoleMember || got.OnBehalfOf.Agent {
				t.Fatalf("delegated principal = %+v / %+v", got, got.OnBehalfOf)
			}
		})
	}
}
