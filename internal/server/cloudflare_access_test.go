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
	person, err := cloudflareAccessPrincipal("person-subject", "person@example.com", "")
	if err != nil || person != "person-subject" {
		t.Fatalf("person principal = %q, %v", person, err)
	}
	service, err := cloudflareAccessPrincipal("", "", "service-client.access")
	if err != nil || service != "service_token:service-client.access" {
		t.Fatalf("service principal = %q, %v", service, err)
	}
	for _, claims := range [][3]string{
		{"", "", ""},
		{"", "person@example.com", "service-client.access"},
		{"person-subject", "", ""},
		{"person-subject", "person@example.com\nspoof", ""},
	} {
		if principal, err := cloudflareAccessPrincipal(claims[0], claims[1], claims[2]); err == nil {
			t.Fatalf("unsafe claims accepted as %q: %#v", principal, claims)
		}
	}
}

var _ cloudflareAccessVerifier = fakeCloudflareAccessVerifier{}
