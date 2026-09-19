package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func TestBlockingEscalationEndsRunAndAnswerQueuesReplacement(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Choose implementation", Checklist: []string{"Implement"}, IdempotencyKey: "start-escalation-test"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	escalation, err := tasks.CreateEscalationFor(t.Context(), started.Task.ID, model.CreateEscalationRequest{
		RunID: started.Run.ID, ExpectedVersion: started.Task.Version, Question: "Which API shape should we use?", Options: []string{"REST", "GraphQL"}, Recommendation: "REST", Blocking: true, IdempotencyKey: "escalate-api-shape",
	}, agent)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != model.TaskWaiting || waiting.WaitingFor == "" || waiting.Version != started.Task.Version+1 || waiting.Runs[0].Status != model.TaskWaiting || waiting.Runs[0].EndedAt == nil {
		t.Fatalf("waiting task = %+v", waiting)
	}
	if _, err := tasks.HeartbeatFor(t.Context(), waiting.ID, started.Run.ID, agent); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("heartbeat after escalation = %v, want not found", err)
	}
	answered, err := tasks.ResolveEscalationFor(t.Context(), waiting.ID, escalation.ID, model.ResolveEscalationRequest{
		ExpectedVersion: waiting.Version, Answer: "Use REST.", SelectedOption: "REST", IdempotencyKey: "answer-api-shape",
	}, HumanPrincipal("human:operator"))
	if err != nil {
		t.Fatal(err)
	}
	if answered.Status != model.EscalationAnswered || answered.AnswerMessageID == "" || answered.SelectedOption != "REST" || answered.ResolvedBy != "human:operator" {
		t.Fatalf("answered escalation = %+v", answered)
	}
	queued, err := tasks.GetFor(t.Context(), waiting.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != model.TaskQueued || queued.Owner != "" || queued.WaitingFor != "" || queued.Version != waiting.Version+1 {
		t.Fatalf("queued task = %+v", queued)
	}
	replacement, err := tasks.ClaimFor(t.Context(), queued.ID, model.ClaimRequest{ExpectedVersion: queued.Version, IdempotencyKey: "replacement-claim"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Run.ID == started.Run.ID || replacement.Run.Status != model.TaskActive {
		t.Fatalf("replacement run = %+v", replacement.Run)
	}
	messages, err := tasks.ListMessagesFor(t.Context(), queued.ID, "", 10, agent)
	if err != nil || len(messages) != 2 {
		t.Fatalf("escalation messages = %+v, %v", messages, err)
	}
}

func TestNonBlockingEscalationLeavesRunActiveAndCapabilityIsRequired(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Ask while working", Checklist: []string{"Implement"}, IdempotencyKey: "start-nonblocking-test"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	escalation, err := tasks.CreateEscalationFor(t.Context(), started.Task.ID, model.CreateEscalationRequest{
		RunID: started.Run.ID, ExpectedVersion: started.Task.Version, Question: "Any preference?", Blocking: false, IdempotencyKey: "nonblocking-question",
	}, agent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != model.TaskActive || current.Version != started.Task.Version || current.Runs[0].EndedAt != nil {
		t.Fatalf("non-blocking task = %+v", current)
	}
	if _, err := tasks.HeartbeatFor(t.Context(), current.ID, started.Run.ID, agent); err != nil {
		t.Fatalf("heartbeat after non-blocking escalation: %v", err)
	}
	if _, err := tasks.ResolveEscalationFor(t.Context(), current.ID, escalation.ID, model.ResolveEscalationRequest{ExpectedVersion: current.Version, Answer: "No preference.", SelectedOption: "missing"}, HumanPrincipal("human:operator")); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid choice error = %v, want validation", err)
	}
	policy := DefaultAgentPolicy()
	delete(policy.Capabilities, CapabilityTaskEscalate)
	if _, err := tasks.CreateEscalationFor(t.Context(), current.ID, model.CreateEscalationRequest{RunID: started.Run.ID, ExpectedVersion: current.Version, Question: "Denied", IdempotencyKey: "denied-question"}, AgentPrincipalWithPolicy(agent.ID, policy)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing capability error = %v, want forbidden", err)
	}
}
