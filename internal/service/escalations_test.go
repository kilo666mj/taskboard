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

func escalateForDecision(t *testing.T, tasks *Service, key string, request model.CreateEscalationRequest) (model.Task, model.TaskEscalation) {
	t.Helper()
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Remediate " + key, Checklist: []string{"Apply"}, IdempotencyKey: "start-" + key}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request.RunID, request.ExpectedVersion, request.IdempotencyKey = started.Run.ID, started.Task.Version, "escalate-"+key
	if request.Question == "" {
		request.Question = "Apply the proposed fix?"
	}
	escalation, err := tasks.CreateEscalationFor(t.Context(), started.Task.ID, request, agent)
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	return task, escalation
}

func TestEscalationAnswerersRestrictWhoMayAnswer(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, escalation := escalateForDecision(t, tasks, "answerers", model.CreateEscalationRequest{Options: []string{"Approve", "Reject"}, Blocking: true, Answerers: []string{" human:approver ", "human:approver"}})
	if len(escalation.Answerers) != 1 || escalation.Answerers[0] != "human:approver" {
		t.Fatalf("answerers = %v", escalation.Answerers)
	}
	answer := model.ResolveEscalationRequest{ExpectedVersion: task.Version, Answer: "Approved.", SelectedOption: "Approve"}
	if _, err := tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, answer, HumanPrincipal("human:bystander")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unnamed answer error = %v", err)
	}
	answered, err := tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, answer, HumanPrincipal("human:approver"))
	if err != nil || answered.ResolvedBy != "human:approver" || answered.DelegatedBy != "" {
		t.Fatalf("named answer = %+v, %v", answered, err)
	}
}

func TestEscalationPolicyValidation(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Validate", Checklist: []string{"Ask"}, IdempotencyKey: "start-validate"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, 21)
	for index := range tooMany {
		tooMany[index] = "human:person-" + string(rune('a'+index))
	}
	for name, request := range map[string]model.CreateEscalationRequest{
		"agent answerer":   {Answerers: []string{"agent:worker"}},
		"service answerer": {Answerers: []string{"cloudflare_access:service_token:abc"}},
		"too many":         {Answerers: tooMany},
		"short expiry":     {ExpiresIn: 30},
		"long expiry":      {ExpiresIn: 8 * 24 * 60 * 60},
	} {
		request.RunID, request.ExpectedVersion, request.Question = started.Run.ID, started.Task.Version, "Question?"
		if _, err := tasks.CreateEscalationFor(t.Context(), started.Task.ID, request, agent); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func TestExpiredEscalationRefusesAnswers(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, escalation := escalateForDecision(t, tasks, "expiry", model.CreateEscalationRequest{Blocking: true, ExpiresIn: 3600})
	if escalation.ExpiresAt == nil || escalation.ExpiresAt.Sub(escalation.CreatedAt) != time.Hour || escalation.Status != model.EscalationOpen {
		t.Fatalf("escalation = %+v", escalation)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE task_escalations SET expires_at=? WHERE id=?`, past, escalation.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := tasks.ListEscalationsFor(t.Context(), task.ID, HumanPrincipal("human:operator"))
	if err != nil || len(listed) != 1 || listed[0].Status != model.EscalationExpired {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	_, err = tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, model.ResolveEscalationRequest{ExpectedVersion: task.Version, Answer: "Too late."}, HumanPrincipal("human:operator"))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expired answer error = %v", err)
	}
	unchanged, err := tasks.GetFor(t.Context(), task.ID, HumanPrincipal("human:operator"))
	if err != nil || unchanged.Status != model.TaskWaiting {
		t.Fatalf("task after refused answer = %+v, %v", unchanged, err)
	}
}

func TestDelegatedAnswersRequireAllowlistAndNamedAnswerer(t *testing.T) {
	tasks := testService(t, time.Minute)
	tasks.SetAnswerDelegation([]string{"agent:notifier"}, RoleMember)
	delegate, stranger := AgentPrincipal("agent:notifier"), AgentPrincipal("agent:other")
	task, escalation := escalateForDecision(t, tasks, "delegated", model.CreateEscalationRequest{Options: []string{"Approve", "Reject"}, Blocking: true, Answerers: []string{"human:approver", "human:former"}})
	open, openEscalation := escalateForDecision(t, tasks, "unrestricted", model.CreateEscalationRequest{Blocking: true})
	answer := model.ResolveEscalationRequest{ExpectedVersion: task.Version, Answer: "Approved from the card.", SelectedOption: "Approve", IdempotencyKey: "delegated-approve"}

	if _, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "human:approver", answer, stranger); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unlisted delegate error = %v", err)
	}
	if _, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), open.ID, openEscalation.ID, "human:approver", model.ResolveEscalationRequest{ExpectedVersion: open.Version, Answer: "Yes."}, delegate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unrestricted escalation error = %v", err)
	}
	if _, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "human:bystander", answer, delegate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unnamed person error = %v", err)
	}
	if _, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "agent:worker", answer, delegate); !errors.Is(err, ErrValidation) {
		t.Fatalf("agent person error = %v", err)
	}
	if err := tasks.store.OffboardPrincipal(t.Context(), "human:former", "human:admin", "left"); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "human:former", answer, delegate); !errors.Is(err, ErrForbidden) {
		t.Fatalf("offboarded person error = %v", err)
	}

	answered, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "human:approver", answer, delegate)
	if err != nil {
		t.Fatal(err)
	}
	if answered.ResolvedBy != "human:approver" || answered.DelegatedBy != "agent:notifier" || answered.SelectedOption != "Approve" {
		t.Fatalf("delegated answer = %+v", answered)
	}
	replayed, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "human:approver", answer, delegate)
	if err != nil || replayed.AnswerMessageID != answered.AnswerMessageID {
		t.Fatalf("replayed delegated answer = %+v, %v", replayed, err)
	}
	messages, err := tasks.ListMessagesFor(t.Context(), task.ID, "", 10, HumanPrincipal("human:approver"))
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == answered.AnswerMessageID && message.Author != "human:approver" {
			t.Fatalf("answer author = %q", message.Author)
		}
	}
	queued, err := tasks.GetFor(t.Context(), task.ID, delegate)
	if err != nil || queued.Status != model.TaskQueued {
		t.Fatalf("task after delegated answer = %+v, %v", queued, err)
	}
}

func TestDelegatedPersonWithoutWriteRoleCannotAnswer(t *testing.T) {
	tasks := testService(t, time.Minute)
	tasks.SetAnswerDelegation([]string{"agent:notifier"}, RoleViewer)
	task, escalation := escalateForDecision(t, tasks, "viewer", model.CreateEscalationRequest{Blocking: true, Answerers: []string{"human:approver"}})
	_, err := tasks.ResolveEscalationOnBehalfFor(t.Context(), task.ID, escalation.ID, "human:approver", model.ResolveEscalationRequest{ExpectedVersion: task.Version, Answer: "Yes."}, AgentPrincipal("agent:notifier"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer delegated answer error = %v", err)
	}
}

func TestSelectedChoiceIsACompleteAnswer(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, escalation := escalateForDecision(t, tasks, "choice-only", model.CreateEscalationRequest{Options: []string{"Approve fix", "Reject"}, Blocking: true})
	person := HumanPrincipal("human:operator")
	if _, err := tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, model.ResolveEscalationRequest{ExpectedVersion: task.Version}, person); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty answer without a choice error = %v", err)
	}
	if _, err := tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, model.ResolveEscalationRequest{ExpectedVersion: task.Version, SelectedOption: "Maybe"}, person); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown choice error = %v", err)
	}
	answered, err := tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, model.ResolveEscalationRequest{ExpectedVersion: task.Version, SelectedOption: "Approve fix"}, person)
	if err != nil || answered.SelectedOption != "Approve fix" {
		t.Fatalf("choice-only answer = %+v, %v", answered, err)
	}
	messages, err := tasks.ListMessagesFor(t.Context(), task.ID, "", 10, person)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.ID == answered.AnswerMessageID && message.Body != "Approve fix" {
			t.Fatalf("answer body = %q, want the selected choice", message.Body)
		}
	}
}

func TestMarkDecisionsForShowsOnlyQuestionsTheViewerMayAnswer(t *testing.T) {
	tasks := testService(t, time.Minute)
	named, _ := escalateForDecision(t, tasks, "needs-me-named", model.CreateEscalationRequest{Blocking: true, Answerers: []string{"human:approver"}})
	open, _ := escalateForDecision(t, tasks, "needs-me-open", model.CreateEscalationRequest{Blocking: true})
	expired, expiredEscalation := escalateForDecision(t, tasks, "needs-me-expired", model.CreateEscalationRequest{Blocking: true, ExpiresIn: 3600})
	answered, answeredEscalation := escalateForDecision(t, tasks, "needs-me-answered", model.CreateEscalationRequest{Blocking: true})
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE task_escalations SET expires_at=? WHERE id=?`, past, expiredEscalation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ResolveEscalationFor(t.Context(), answered.ID, answeredEscalation.ID, model.ResolveEscalationRequest{ExpectedVersion: answered.Version, Answer: "Done."}, HumanPrincipal("human:other")); err != nil {
		t.Fatal(err)
	}
	flags := func(principal Principal) map[string]bool {
		items := []model.Task{named, open, expired, answered}
		if err := tasks.MarkDecisionsFor(t.Context(), items, principal); err != nil {
			t.Fatal(err)
		}
		result := map[string]bool{}
		for _, item := range items {
			result[item.ID] = item.DecisionRequested
		}
		return result
	}
	approver := flags(HumanPrincipal("human:approver"))
	if !approver[named.ID] || !approver[open.ID] || approver[expired.ID] || approver[answered.ID] {
		t.Fatalf("approver flags = %v", approver)
	}
	bystander := flags(HumanPrincipal("human:bystander"))
	if bystander[named.ID] || !bystander[open.ID] {
		t.Fatalf("bystander flags = %v", bystander)
	}
	if viewer := flags(HumanPrincipalWithRole("human:approver", RoleViewer)); viewer[named.ID] || viewer[open.ID] {
		t.Fatalf("viewer flags = %v", viewer)
	}
	if agent := flags(AgentPrincipal("agent:worker")); agent[named.ID] || agent[open.ID] {
		t.Fatalf("agent flags = %v", agent)
	}
}
