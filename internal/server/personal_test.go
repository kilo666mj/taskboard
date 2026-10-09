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
		name      string
		token     string
		headers   map[string]string
		want      int
		delegated bool
	}{
		{name: "agent credential", token: worker, want: http.StatusNoContent},
		{name: "shared bearer", token: shared, want: http.StatusNoContent},
		{name: "forwarded operator", token: switchboard, headers: map[string]string{switchboardOAuthSubjectHeader: personalOperator}, want: http.StatusNoContent, delegated: true},
		{name: "forwarded operator from another credential", token: worker, headers: map[string]string{switchboardOAuthSubjectHeader: personalOperator}, want: http.StatusNoContent, delegated: true},
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
			if !got.Agent || got.Operator != personalOperator {
				t.Fatalf("principal = %+v, want an agent of the operator", got)
			}
			if !test.delegated {
				if got.OnBehalfOf != nil {
					t.Fatalf("agent without a forwarded person acts for %+v", got.OnBehalfOf)
				}
				return
			}
			if got.OnBehalfOf == nil || got.OnBehalfOf.ID != personalOperator || got.OnBehalfOf.Role != service.RoleOwner {
				t.Fatalf("delegated principal = %+v, want the owner", got.OnBehalfOf)
			}
		})
	}

	// Work an agent records for the forwarded operator is their private task,
	// and every other agent can see and list it.
	forwarded, _ := authenticate(t, switchboard, map[string]string{switchboardOAuthSubjectHeader: personalOperator})
	private, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Renew the boat licence"}, forwarded)
	if err != nil {
		t.Fatal(err)
	}
	if private.Visibility != model.VisibilityPrivate || private.CreatedBy != personalOperator {
		t.Fatalf("forwarded task = %s by %q, want private by the operator", private.Visibility, private.CreatedBy)
	}
	other, _ := authenticate(t, worker, nil)
	if _, err := tasks.GetFor(t.Context(), private.ID, other); err != nil {
		t.Fatalf("another agent could not see the operator's task: %v", err)
	}
	page, err := tasks.ListFor(t.Context(), model.ListTasksRequest{}, other)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, task := range page.Tasks {
		listed = listed || task.ID == private.ID
	}
	if !listed {
		t.Fatal("another agent's listing left out the operator's private task")
	}

	// Without a forwarded person an agent still records work as itself, so
	// routine producers keep their attribution and pickup work stays in the
	// agent lane.
	own, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Update the base image"}, other)
	if err != nil {
		t.Fatal(err)
	}
	if own.Visibility != model.VisibilityAgent || own.CreatedBy != "agent:worker" {
		t.Fatalf("agent task = %s by %q, want agent lane by agent:worker", own.Visibility, own.CreatedBy)
	}

	// The operator's private tasks stay hidden outside personal mode.
	if _, err := tasks.GetFor(t.Context(), private.ID, agentPrincipal(config.Config{}, "agent:worker")); err == nil {
		t.Fatal("an agent outside personal mode saw the operator's private task")
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
