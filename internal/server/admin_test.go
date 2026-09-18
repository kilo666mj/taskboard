package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/service"
)

func adminRequest(t *testing.T, method, target, body string, role service.Role) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	return request.WithContext(context.WithValue(request.Context(), principalKey{}, service.HumanPrincipalWithRole("admin@example.com", role)))
}

func TestGeneratedAgentCredentialAuthenticatesAndOffboardingRevokes(t *testing.T) {
	_, database, _ := serverFixture(t)
	_, token, err := database.CreateAgentCredential(t.Context(), "Build", "agent:build", nil, "owner")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, false, logger)
	cfg := config.Config{AuthToken: "0123456789abcdef0123456789abcdef", LeaseDuration: time.Minute}
	handler := auth(cfg, sessions, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Actor", actor(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://taskboard.test/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call(); response.Code != http.StatusNoContent || response.Header().Get("X-Actor") != "agent:build" {
		t.Fatalf("credential auth status/actor = %d/%q", response.Code, response.Header().Get("X-Actor"))
	}
	if err := database.OffboardPrincipal(t.Context(), "agent:build", "owner", "retired"); err != nil {
		t.Fatal(err)
	}
	if response := call(); response.Code != http.StatusUnauthorized {
		t.Fatalf("offboarded auth status = %d, want 401", response.Code)
	}
}

func TestAdministrativeCredentialLifecycleIsRoleProtected(t *testing.T) {
	_, database, _ := serverFixture(t)
	viewerRequest := adminRequest(t, http.MethodPost, "/api/v1/admin/credentials", `{"name":"Build","principal_id":"agent:build"}`, service.RoleViewer)
	viewerResponse := httptest.NewRecorder()
	createAgentCredential(database).ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer status = %d, want 403", viewerResponse.Code)
	}

	request := adminRequest(t, http.MethodPost, "/api/v1/admin/credentials", `{"name":"Build","principal_id":"agent:build"}`, service.RoleAdmin)
	response := httptest.NewRecorder()
	createAgentCredential(database).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", response.Code, response.Body.String())
	}
	var created struct {
		Token      string `json:"token"`
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.Token == "" || created.Credential.ID == "" {
		t.Fatalf("created credential = %+v, %v", created, err)
	}

	listRequest := adminRequest(t, http.MethodGet, "/api/v1/admin/credentials", "", service.RoleAdmin)
	listResponse := httptest.NewRecorder()
	listAgentCredentials(database).ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || bytes.Contains(listResponse.Body.Bytes(), []byte(created.Token)) {
		t.Fatalf("list status/body = %d/%s", listResponse.Code, listResponse.Body.String())
	}

	request = adminRequest(t, http.MethodPost, "/api/v1/admin/principals/agent:build/offboard", `{"reason":"retired","confirm":"agent:build"}`, service.RoleOwner)
	request.SetPathValue("principal", "agent:build")
	response = httptest.NewRecorder()
	offboardPrincipal(database).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("offboard status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, valid, _ := database.AuthenticateAgentCredential(t.Context(), created.Token); valid {
		t.Fatal("offboarded token remained valid")
	}
	audit, err := database.ListAdminAudit(t.Context(), 10)
	if err != nil || len(audit) < 2 {
		t.Fatalf("audit = %+v, %v", audit, err)
	}
}
