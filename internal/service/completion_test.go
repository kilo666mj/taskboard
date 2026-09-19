package service

import (
	"errors"
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
