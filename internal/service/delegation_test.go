package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func delegatingAgent(id string, role Role) Principal {
	person := HumanPrincipalWithRole(id, role)
	agent := AgentPrincipal(id)
	agent.OnBehalfOf = &person
	return agent
}

func TestDelegatedCreateActsAsPersonUnlessAgentLaneRequested(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := delegatingAgent("cloudflare_access:person", RoleMember)
	person := *agent.OnBehalfOf

	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Mine", IdempotencyKey: "delegated-create"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if task.Visibility != model.VisibilityPrivate || task.CreatedBy != person.ID || task.Owner != "" {
		t.Fatalf("delegated task = %+v", task)
	}
	if _, err := tasks.GetFor(t.Context(), task.ID, person); err != nil {
		t.Fatalf("person cannot view delegated task: %v", err)
	}
	events, err := tasks.store.ListTaskEvents(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Payload["delegated_via"] != "mcp" {
		t.Fatalf("created events = %+v", events)
	}
	replayed, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Mine", IdempotencyKey: "delegated-create"}, agent)
	if err != nil || replayed.ID != task.ID {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}

	team, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Shared", Visibility: model.VisibilityTeam}, agent)
	if err != nil || team.Visibility != model.VisibilityTeam {
		t.Fatalf("team task = %+v, %v", team, err)
	}

	queued, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Pickup", Visibility: model.VisibilityAgent}, agent)
	if err != nil || queued.Visibility != model.VisibilityAgent {
		t.Fatalf("agent-lane task = %+v, %v", queued, err)
	}
	queuedEvents, err := tasks.store.ListTaskEvents(t.Context(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queuedEvents) == 0 || queuedEvents[0].Payload["delegated_via"] != nil {
		t.Fatalf("agent-lane events = %+v", queuedEvents)
	}
}

func TestDelegatedCreateRequiresPersonWritePermissionAndAgentCapability(t *testing.T) {
	tasks := testService(t, time.Minute)
	viewer := delegatingAgent("cloudflare_access:viewer", RoleViewer)
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Blocked"}, viewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer delegated create error = %v", err)
	}

	limited := delegatingAgent("cloudflare_access:limited", RoleMember)
	limited.Policy.Capabilities = map[string]bool{CapabilityTaskRead: true}
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Blocked"}, limited); !errors.Is(err, ErrForbidden) {
		t.Fatalf("capability-limited delegated create error = %v", err)
	}
}

func TestDelegationDoesNotApplyToStart(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := delegatingAgent("cloudflare_access:person", RoleMember)
	if _, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Run", Checklist: []string{"step"}, Visibility: model.VisibilityPrivate}, agent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delegated private start error = %v", err)
	}
}
