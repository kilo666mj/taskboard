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

func TestInboxWaitReturnsWhenTheInboxChanges(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent, person := AgentPrincipal("agent:worker"), HumanPrincipal("human:operator")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Waiting", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: started.Task.ID, RunID: started.Run.ID}}}
	before, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || before.Digest == "" {
		t.Fatalf("inbox = %+v, %v", before, err)
	}

	request.WaitSeconds, request.Digest = 20, before.Digest
	go func() {
		time.Sleep(50 * time.Millisecond)
		if _, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{TargetRunID: started.Run.ID, Kind: model.MessageInstruction, Body: "Check the logs"}, person); err != nil {
			t.Error(err)
		}
	}()
	began := time.Now()
	after, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Fatalf("wait took %s; want an early return on the new message", elapsed)
	}
	if after.Digest == before.Digest || len(after.Messages) != 1 {
		t.Fatalf("inbox after message = %+v", after)
	}
}

func TestInboxWaitAnswersAtOnceWhenTheDigestIsStale(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Stale", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: started.Task.ID, RunID: started.Run.ID}}, WaitSeconds: 20, Digest: "previous"}
	began := time.Now()
	inbox, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || inbox.Digest == "previous" || time.Since(began) > 5*time.Second {
		t.Fatalf("inbox = %+v, %v after %s", inbox, err, time.Since(began))
	}
}

func TestInboxWaitEndsUnchangedAfterTheWait(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Quiet", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	other, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Busy elsewhere", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: started.Task.ID, RunID: started.Run.ID}}}
	before, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil {
		t.Fatal(err)
	}
	request.WaitSeconds, request.Digest = 1, before.Digest
	go func() {
		time.Sleep(50 * time.Millisecond)
		if _, err := tasks.AddMessageFor(t.Context(), other.Task.ID, model.AddMessageRequest{AuthorRunID: other.Run.ID, Kind: model.MessageNote, Body: "Unrelated"}, agent); err != nil {
			t.Error(err)
		}
	}()
	began := time.Now()
	after, err := tasks.InboxFor(t.Context(), request, agent)
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("inbox = %+v, %v", after, err)
	}
	if elapsed := time.Since(began); elapsed < 900*time.Millisecond {
		t.Fatalf("returned after %s; an unrelated task's event must not end the wait", elapsed)
	}
}

func TestInboxWaitRejectsOutOfRangeWaits(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Range", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, wait := range []int{-1, inboxMaxWaitSeconds + 1} {
		request := model.InboxRequest{Runs: []model.InboxRun{{TaskID: started.Task.ID, RunID: started.Run.ID}}, WaitSeconds: wait}
		if _, err := tasks.InboxFor(t.Context(), request, agent); !errors.Is(err, ErrValidation) {
			t.Fatalf("wait %d error = %v", wait, err)
		}
	}
}

func TestInboxWaitersAreBoundedPerPrincipal(t *testing.T) {
	tasks := testService(t, time.Minute)
	for range inboxWaitersPerPrincipal {
		if !tasks.acquireInboxWaiter("agent:worker") {
			t.Fatal("acquire within the bound failed")
		}
	}
	if tasks.acquireInboxWaiter("agent:worker") {
		t.Fatal("acquire beyond the bound succeeded")
	}
	if !tasks.acquireInboxWaiter("agent:other") {
		t.Fatal("another principal was limited")
	}
	tasks.releaseInboxWaiter("agent:worker")
	if !tasks.acquireInboxWaiter("agent:worker") {
		t.Fatal("released slot was not reusable")
	}
}
