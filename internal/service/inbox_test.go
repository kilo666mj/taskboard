package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestInboxGathersWaitingWorkForTheNamedRunsOnly(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent, person := AgentPrincipal("agent:worker"), HumanPrincipal("human:operator")
	mine, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Mine", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	other, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Another session", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: mine.Task.ID, RunID: mine.Run.ID}}}

	empty, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || empty.Count != 0 || len(empty.Runs) != 1 || !empty.Runs[0].Active || empty.Runs[0].TaskVersion != mine.Task.Version {
		t.Fatalf("empty inbox = %+v, %v", empty, err)
	}

	for _, started := range []model.StartResult{mine, other} {
		if _, err := tasks.CreateRunControlFor(t.Context(), started.Task.ID, model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlPause, ExpectedVersion: started.Task.Version}, person); err != nil {
			t.Fatal(err)
		}
		if _, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{TargetRunID: started.Run.ID, Kind: model.MessageInstruction, Body: "Run the focused tests"}, person); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{DiscussionCapability}, TTLSeconds: 120}, agent); err != nil {
		t.Fatal(err)
	}
	discussion, err := tasks.StartDiscussionFor(t.Context(), mine.Task.ID, model.StartDiscussionRequest{Message: "Why this host?"}, person)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.StartDiscussionFor(t.Context(), other.Task.ID, model.StartDiscussionRequest{}, person); err != nil {
		t.Fatal(err)
	}

	inbox, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Count != 3 || len(inbox.Controls) != 1 || inbox.Controls[0].TargetRunID != mine.Run.ID ||
		len(inbox.Messages) != 1 || inbox.Messages[0].TaskID != mine.Task.ID ||
		len(inbox.Discussions) != 1 || inbox.Discussions[0].ID != discussion.ID || len(inbox.Discussions[0].Messages) != 1 {
		t.Fatalf("inbox = %+v", inbox)
	}

	if _, err := tasks.UpdateDiscussionFor(t.Context(), discussion.ID, model.UpdateDiscussionRequest{Status: model.DiscussionActive}, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AddDiscussionMessageFor(t.Context(), discussion.ID, model.DiscussionMessageRequest{Body: "It hosts the backups."}, agent); err != nil {
		t.Fatal(err)
	}
	if replied, err := tasks.InboxFor(t.Context(), request, agent); err != nil || len(replied.Discussions) != 0 {
		t.Fatalf("discussion still waiting after reply = %+v, %v", replied.Discussions, err)
	}
	if _, err := tasks.AddDiscussionMessageFor(t.Context(), discussion.ID, model.DiscussionMessageRequest{Body: "Which backups?"}, person); err != nil {
		t.Fatal(err)
	}
	followUp, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || len(followUp.Discussions) != 1 || len(followUp.Discussions[0].Messages) != 1 || followUp.Discussions[0].Messages[0].Body != "Which backups?" {
		t.Fatalf("follow-up discussion = %+v, %v", followUp.Discussions, err)
	}
}

func TestInboxReportsAnsweredEscalationsOnEndedRuns(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	task, escalation := escalateForDecision(t, tasks, "inbox", model.CreateEscalationRequest{Blocking: true})
	request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: task.ID, RunID: escalation.RunID}}}

	open, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || open.Count != 0 || open.Runs[0].Active || open.Runs[0].TaskStatus != model.TaskWaiting {
		t.Fatalf("inbox while waiting = %+v, %v", open, err)
	}
	if _, err := tasks.ResolveEscalationFor(t.Context(), task.ID, escalation.ID, model.ResolveEscalationRequest{ExpectedVersion: task.Version, Answer: "Go ahead."}, HumanPrincipal("human:operator")); err != nil {
		t.Fatal(err)
	}
	answered, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || answered.Count != 1 || answered.Escalations[0].ID != escalation.ID || answered.Escalations[0].Status != model.EscalationAnswered || answered.Runs[0].TaskStatus != model.TaskQueued {
		t.Fatalf("inbox after answer = %+v, %v", answered, err)
	}
}

func TestInboxRejectsPeopleAndRunsOfOtherAgents(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Owned", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: started.Task.ID, RunID: started.Run.ID}}}
	if _, err := tasks.InboxFor(t.Context(), request, HumanPrincipal("human:operator")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("person inbox error = %v", err)
	}
	if _, err := tasks.InboxFor(t.Context(), request, AgentPrincipal("agent:other")); !errors.Is(err, ErrValidation) {
		t.Fatalf("other agent inbox error = %v", err)
	}
	if _, err := tasks.InboxFor(t.Context(), model.InboxRequest{}, agent); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty request error = %v", err)
	}
	if _, err := tasks.InboxFor(t.Context(), model.InboxRequest{Runs: []model.InboxRun{{TaskID: started.Task.ID, RunID: "missing"}}}, agent); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown run error = %v", err)
	}
}
