package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestCompletionContractGatesDoneAndRequiresHumanVerification(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	human := HumanPrincipal("human:owner")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Contract", Checklist: []string{"Ship"}, IdempotencyKey: "contract-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := tasks.CreateCompletionRequirementFor(t.Context(), started.Task.ID, model.CreateCompletionRequirementRequest{Kind: model.RequirementGreenCI, Label: "CI is green", Required: true, ExpectedVersion: started.Task.Version}, human)
	if err != nil {
		t.Fatal(err)
	}
	current, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastEditedBy != human.ID {
		t.Fatalf("completion gate editor = %q, want %q", current.LastEditedBy, human.ID)
	}
	_, err = tasks.UpdateFor(t.Context(), current.ID, model.UpdateRequest{ExpectedVersion: current.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{current.Items[0].ID}, IdempotencyKey: "blocked-completion"}, agent)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("completion error = %v", err)
	}
	reference, err := tasks.AddTaskReferenceFor(t.Context(), current.ID, model.AddTaskReferenceRequest{RunID: started.Run.ID, Kind: model.ReferenceCIRun, Label: "CI #7", URL: "https://ci.example.test/runs/7", IdempotencyKey: "ci-reference"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tasks.SubmitCompletionEvidenceFor(t.Context(), current.ID, requirement.ID, model.SubmitCompletionEvidenceRequest{RunID: started.Run.ID, ReferenceID: reference.ID, Note: "All required jobs passed", IdempotencyKey: "ci-evidence"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	current, err = tasks.GetFor(t.Context(), current.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, ExpectedVersion: current.Version}, human)
	if err != nil || reviewed.Status != model.RequirementSatisfied || reviewed.Evidence[0].Status != model.EvidenceVerified {
		t.Fatalf("reviewed = %+v, %v", reviewed, err)
	}
	current, err = tasks.GetFor(t.Context(), current.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastEditedBy != human.ID {
		t.Fatalf("completion review editor = %q, want %q", current.LastEditedBy, human.ID)
	}
	done, err := tasks.UpdateFor(t.Context(), current.ID, model.UpdateRequest{ExpectedVersion: current.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{current.Items[0].ID}, IdempotencyKey: "accepted-completion"}, agent)
	if err != nil || done.Status != model.TaskDone {
		t.Fatalf("done = %+v, %v", done, err)
	}
}

func TestCompletionEvidenceRejectsWrongReferenceKind(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	human := HumanPrincipal("human:owner")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Wrong evidence", Checklist: []string{"Ship"}, IdempotencyKey: "wrong-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := tasks.CreateCompletionRequirementFor(t.Context(), started.Task.ID, model.CreateCompletionRequirementRequest{Kind: model.RequirementPullRequest, Label: "PR", Required: true, ExpectedVersion: started.Task.Version}, human)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := tasks.AddTaskReferenceFor(t.Context(), started.Task.ID, model.AddTaskReferenceRequest{RunID: started.Run.ID, Kind: model.ReferenceCIRun, Label: "CI", URL: "https://ci.example.test/1", IdempotencyKey: "wrong-ref"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tasks.SubmitCompletionEvidenceFor(t.Context(), started.Task.ID, requirement.ID, model.SubmitCompletionEvidenceRequest{RunID: started.Run.ID, ReferenceID: reference.ID, IdempotencyKey: "wrong-evidence"}, agent)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("wrong kind error = %v", err)
	}
}

func TestCompletionReviewRejectsBuilderAndRecordsReason(t *testing.T) {
	tasks := testService(t, time.Minute)
	builder := AgentPrincipal("agent:builder")
	human := HumanPrincipal("human:owner")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Separated review", Checklist: []string{"Ship"}, IdempotencyKey: "separated-start"}, builder)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := tasks.CreateCompletionRequirementFor(t.Context(), started.Task.ID, model.CreateCompletionRequirementRequest{Kind: model.RequirementValidation, Label: "Validation", Required: true, ExpectedVersion: started.Task.Version}, human)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tasks.SubmitCompletionEvidenceFor(t.Context(), started.Task.ID, requirement.ID, model.SubmitCompletionEvidenceRequest{RunID: started.Run.ID, Note: "Validated locally", IdempotencyKey: "separated-evidence"}, builder)
	if err != nil {
		t.Fatal(err)
	}
	current, err := tasks.GetFor(t.Context(), started.Task.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, ExpectedVersion: current.Version}, builder); !errors.Is(err, ErrForbidden) {
		t.Fatalf("builder without validation capability error = %v", err)
	}
	validator := validatorPrincipal(builder.ID)
	if listed, listErr := tasks.ListCompletionRequirementsFor(t.Context(), current.ID, validator); listErr != nil || len(listed) != 1 {
		t.Fatalf("validator completion list = %+v, %v", listed, listErr)
	}
	if _, err = tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, ExpectedVersion: current.Version}, validator); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "idempotency_key") {
		t.Fatalf("validator missing idempotency error = %v", err)
	}
	request := model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, ExpectedVersion: current.Version, IdempotencyKey: "self-review-rejected"}
	_, err = tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, request, validator)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "builder provenance") {
		t.Fatalf("self-review error = %v", err)
	}
	if _, err = tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, request, validator); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "builder provenance") {
		t.Fatalf("replayed self-review error = %v", err)
	}
	after, err := tasks.GetFor(t.Context(), current.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != current.Version {
		t.Fatalf("version = %d, want unchanged %d", after.Version, current.Version)
	}
	events, err := tasks.store.ListTaskEvents(t.Context(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != "task.completion_review_rejected" || last.Actor != builder.ID || last.Payload["reason_code"] != "builder_validator_same_principal" {
		t.Fatalf("rejection event = %+v", last)
	}
}

func TestCompletionReviewUsesPrincipalProvenanceAcrossReplacementRuns(t *testing.T) {
	tasks := testService(t, time.Minute)
	firstBuilder := AgentPrincipal("agent:first-builder")
	secondBuilder := AgentPrincipal("agent:second-builder")
	human := HumanPrincipal("human:owner")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Replacement run", Checklist: []string{"Ship"}, IdempotencyKey: "replacement-start"}, firstBuilder)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := tasks.CreateCompletionRequirementFor(t.Context(), started.Task.ID, model.CreateCompletionRequirementRequest{Kind: model.RequirementValidation, Label: "Validation", Required: true, ExpectedVersion: started.Task.Version}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET lease_expires_at=? WHERE id=?`, stamp(time.Now().UTC().Add(-time.Hour)), started.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.SweepStale(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale, err := tasks.GetFor(t.Context(), started.Task.ID, secondBuilder)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := tasks.ClaimFor(t.Context(), stale.ID, model.ClaimRequest{ExpectedVersion: stale.Version, IdempotencyKey: "replacement-claim"}, secondBuilder)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tasks.SubmitCompletionEvidenceFor(t.Context(), stale.ID, requirement.ID, model.SubmitCompletionEvidenceRequest{RunID: replacement.Run.ID, Note: "Replacement run validation", IdempotencyKey: "replacement-evidence"}, secondBuilder)
	if err != nil {
		t.Fatal(err)
	}
	current, err := tasks.GetFor(t.Context(), stale.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, ExpectedVersion: current.Version, IdempotencyKey: "prior-builder-review"}, validatorPrincipal(firstBuilder.ID))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("prior builder review error = %v", err)
	}
	reviewRequest := model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, ExpectedVersion: current.Version, IdempotencyKey: "delegated-review"}
	reviewed, err := tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, reviewRequest, validatorPrincipal("agent:delegated-validator"))
	if err != nil || reviewed.VerifiedBy != "agent:delegated-validator" || reviewed.Status != model.RequirementSatisfied {
		t.Fatalf("delegated review = %+v, %v", reviewed, err)
	}
	replayed, err := tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, reviewRequest, validatorPrincipal("agent:delegated-validator"))
	if err != nil || replayed.VerifiedBy != "agent:delegated-validator" {
		t.Fatalf("replayed delegated review = %+v, %v", replayed, err)
	}
}

func TestCompletionReviewOwnerOverrideRequiresAndAuditsReason(t *testing.T) {
	tasks := testService(t, time.Minute)
	builder := HumanPrincipal("human:builder-admin")
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Admin override", Checklist: []string{"Ship"}}, builder)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := tasks.CreateCompletionRequirementFor(t.Context(), created.ID, model.CreateCompletionRequirementRequest{Kind: model.RequirementValidation, Label: "Validation", Required: true, ExpectedVersion: created.Version}, builder)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := tasks.SubmitCompletionEvidenceFor(t.Context(), created.ID, requirement.ID, model.SubmitCompletionEvidenceRequest{Note: "Manual validation"}, builder)
	if err != nil {
		t.Fatal(err)
	}
	current, err := tasks.GetFor(t.Context(), created.ID, builder)
	if err != nil {
		t.Fatal(err)
	}
	request := model.ReviewCompletionRequest{Status: model.RequirementSatisfied, EvidenceID: evidence.ID, SeparationOverride: true, SeparationOverrideReason: "Emergency release approved by the workspace owner", ExpectedVersion: current.Version}
	if _, err = tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, request, builder); !errors.Is(err, ErrValidation) {
		t.Fatalf("member override error = %v", err)
	}
	reviewed, err := tasks.ReviewCompletionRequirementFor(t.Context(), current.ID, requirement.ID, request, HumanPrincipalWithRole(builder.ID, RoleOwner))
	if err != nil || reviewed.Status != model.RequirementSatisfied || reviewed.VerifiedBy != builder.ID {
		t.Fatalf("owner override = %+v, %v", reviewed, err)
	}
	events, err := tasks.store.ListTaskEvents(t.Context(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[len(events)-2].Payload["reason_code"] != "override_not_authorized" {
		t.Fatalf("unauthorized override event = %+v", events)
	}
	last := events[len(events)-1]
	if last.Kind != "task.completion_requirement_reviewed" || last.Payload["separation_override"] != true || last.Payload["separation_override_reason"] != request.SeparationOverrideReason {
		t.Fatalf("override event = %+v", last)
	}
}

func validatorPrincipal(id string) Principal {
	return AgentPrincipalWithPolicy(id, AgentPolicy{Capabilities: map[string]bool{
		CapabilityTaskRead:     true,
		CapabilityTaskValidate: true,
	}, RequireIdempotency: true})
}
