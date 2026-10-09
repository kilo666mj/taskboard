package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

const personalOperator = "c04f726d-operator"

func personalConfig(token string) config.Config {
	return config.Config{
		AuthToken: token, DefaultRole: "owner", LeaseDuration: time.Minute, MCPHumanDelegation: true,
		OIDCIssuer: "https://idp.example.com", OIDCClientID: "taskboard", OIDCRedirectURL: "https://taskboard.example.com/api/v1/auth/oidc/callback",
		OIDCAllowedSubjects: []string{personalOperator}, PersonalOperator: personalOperator,
	}
}

func TestPersonalModeAgentsActForTheOperator(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	sessions := newBrowserSessions(database, true, logger)
	_, switchboard, err := database.CreateAgentCredential(t.Context(), "switchboard", "agent:switchboard", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, worker, err := database.CreateAgentCredential(t.Context(), "worker", "agent:worker", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	shared := strings.Repeat("test-token-", 4)
	cfg := personalConfig(shared)
	authenticate := func(t *testing.T, token string, headers map[string]string) (service.Principal, int) {
		t.Helper()
		var got service.Principal
		handler := auth(cfg, sessions, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = mcpPrincipal(r.Context())
			w.WriteHeader(http.StatusNoContent)
		}))
		request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return got, response.Code
	}

	for _, test := range []struct {
		name    string
		token   string
		headers map[string]string
		want    int
	}{
		{name: "agent credential", token: worker, want: http.StatusNoContent},
		{name: "shared bearer", token: shared, want: http.StatusNoContent},
		{name: "forwarded operator", token: switchboard, headers: map[string]string{switchboardOAuthSubjectHeader: personalOperator}, want: http.StatusNoContent},
		{name: "forwarded other person", token: switchboard, headers: map[string]string{switchboardOAuthSubjectHeader: "someone-else"}, want: http.StatusUnauthorized},
		{name: "forwarded access person", token: switchboard, headers: map[string]string{switchboardAccessSubjectHeader: "cloudflare_access:someone"}, want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, status := authenticate(t, test.token, test.headers)
			if status != test.want {
				t.Fatalf("status = %d, want %d", status, test.want)
			}
			if status != http.StatusNoContent {
				return
			}
			if !got.Agent || got.OnBehalfOf == nil || got.OnBehalfOf.ID != personalOperator || got.OnBehalfOf.Role != service.RoleOwner {
				t.Fatalf("principal = %+v / %+v, want an agent acting for the owner", got, got.OnBehalfOf)
			}
		})
	}

	// Work one agent records for the operator is private to them, and every
	// other agent acting for them can see it.
	creator, _ := authenticate(t, switchboard, nil)
	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Renew the boat licence"}, creator)
	if err != nil {
		t.Fatal(err)
	}
	if task.Visibility != model.VisibilityPrivate || task.CreatedBy != personalOperator {
		t.Fatalf("created task = %s by %q, want private by the operator", task.Visibility, task.CreatedBy)
	}
	other, _ := authenticate(t, worker, nil)
	if _, err := tasks.GetFor(t.Context(), task.ID, other); err != nil {
		t.Fatalf("another agent could not see the operator's task: %v", err)
	}

	if err := database.OffboardPrincipal(t.Context(), personalOperator, "test", "left"); err != nil {
		t.Fatal(err)
	}
	if _, status := authenticate(t, worker, nil); status != http.StatusUnauthorized {
		t.Fatalf("agent of an offboarded operator status = %d, want 401", status)
	}
}

func TestPersonalModeBrowserSignInIsTheOperatorOnly(t *testing.T) {
	_, database, logger := serverFixture(t)
	sessions := newBrowserSessions(database, true, logger)
	cfg := personalConfig(strings.Repeat("test-token-", 4))
	state := sessionState(cfg, sessions, nil)
	handler := auth(cfg, sessions, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if person := principal(r.Context()); person.Agent || person.Role != service.RoleOwner {
			t.Errorf("principal = %+v, want the owner", person)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		subject string
		groups  []string
		want    int
	}{
		// Groups no longer matter: a session without them is still the owner.
		{subject: personalOperator, want: http.StatusNoContent},
		{subject: personalOperator, groups: []string{"viewers"}, want: http.StatusNoContent},
		// A session made before personal mode was enabled stops working.
		{subject: "someone-else", want: http.StatusUnauthorized},
	} {
		token, _, err := database.CreateBrowserSession(t.Context(), store.BrowserIdentity{Subject: test.subject, Groups: test.groups}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/tasks", nil)
		request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("%s: status = %d, want %d", test.subject, response.Code, test.want)
		}
		stateResponse := httptest.NewRecorder()
		state(stateResponse, request)
		authenticated := strings.Contains(stateResponse.Body.String(), `"authenticated":true`)
		if authenticated != (test.want == http.StatusNoContent) {
			t.Errorf("%s: session state = %s", test.subject, stateResponse.Body.String())
		}
	}
}
