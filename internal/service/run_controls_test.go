package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestRunControlLifecycleIsDurableAndOnlyCompletionMutatesTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	human := HumanPrincipal("human:operator")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Pause safely", Checklist: []string{"Work"}, IdempotencyKey: "control-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlPause, Reason: "Wait for approval", ExpectedVersion: started.Task.Version, IdempotencyKey: "pause-request"}
	control, err := tasks.CreateRunControlFor(t.Context(), started.Task.ID, request, human)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := tasks.CreateRunControlFor(t.Context(), started.Task.ID, request, human)
	if err != nil || replayed.ID != control.ID {
		t.Fatalf("replayed control = %+v, %v", replayed, err)
	}
	current, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil || current.Status != model.TaskActive || current.Version != started.Task.Version {
		t.Fatalf("task after request = %+v, %v", current, err)
	}
	pending, err := tasks.ListPendingRunControlsFor(t.Context(), 10, agent)
	if err != nil || len(pending) != 1 || pending[0].ID != control.ID {
		t.Fatalf("controller poll = %+v, %v", pending, err)
	}
	heartbeat, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, agent)
	if err != nil || len(heartbeat.PendingControls) != 1 || heartbeat.PendingControls[0].ID != control.ID {
		t.Fatalf("heartbeat controls = %+v, %v", heartbeat.PendingControls, err)
	}
	acknowledged, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlAcknowledged, IdempotencyKey: "pause-ack"}, agent)
	if err != nil || acknowledged.AcknowledgedAt == nil {
		t.Fatalf("acknowledged control = %+v, %v", acknowledged, err)
	}
	accepted, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlAccepted, OutcomeNote: "Will pause after this operation", IdempotencyKey: "pause-accept"}, agent)
	if err != nil || accepted.DecidedAt == nil {
		t.Fatalf("accepted control = %+v, %v", accepted, err)
	}
	completed, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlCompleted, OutcomeNote: "Paused at a safe point", ExpectedVersion: current.Version, IdempotencyKey: "pause-complete"}, agent)
	if err != nil || completed.CompletedAt == nil {
		t.Fatalf("completed control = %+v, %v", completed, err)
	}
	waiting, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != model.TaskWaiting || waiting.Version != current.Version+1 || waiting.Runs[0].EndedAt == nil || waiting.Runs[0].Status != model.TaskWaiting {
		t.Fatalf("task after completed pause = %+v", waiting)
	}
	var events int
	if err := tasks.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE task_id=? AND kind IN ('task.control_requested','task.control_updated')`, started.Task.ID).Scan(&events); err != nil || events != 4 {
		t.Fatalf("control events = %d, %v", events, err)
	}
}

func TestRetryControlQueuesStaleTaskForFreshClaim(t *testing.T) {
	tasks := testService(t, -time.Second)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Retry stale work", Checklist: []string{"Work"}, IdempotencyKey: "retry-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.SweepStale(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil || stale.Status != model.TaskStale || stale.Runs[0].EndedAt == nil {
		t.Fatalf("stale task = %+v, %v", stale, err)
	}
	control, err := tasks.CreateRunControlFor(t.Context(), stale.ID, model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlRetry, ExpectedVersion: stale.Version}, HumanPrincipal("human:operator"))
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeAndAcceptControl(t, tasks, agent, control.ID)
	if _, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlCompleted, ExpectedVersion: stale.Version, OutcomeNote: "Ready to retry", IdempotencyKey: "retry-complete"}, agent); err != nil {
		t.Fatal(err)
	}
	queued, err := tasks.GetFor(t.Context(), stale.ID, agent)
	if err != nil || queued.Status != model.TaskQueued || queued.Version != stale.Version+1 {
		t.Fatalf("queued retry = %+v, %v", queued, err)
	}
	replacement, err := tasks.ClaimFor(t.Context(), queued.ID, model.ClaimRequest{ExpectedVersion: queued.Version, IdempotencyKey: "retry-replacement"}, agent)
	if err != nil || replacement.Run.ID == started.Run.ID {
		t.Fatalf("replacement = %+v, %v", replacement.Run, err)
	}
}

func TestOperatorReviewRequeuesStaleTaskAndClaimsEditProvenance(t *testing.T) {
	tasks := testService(t, -time.Second)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Recover stale work", Checklist: []string{"Work"}, IdempotencyKey: "review-requeue-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.SweepStale(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	member := HumanPrincipalWithRole("human:member", RoleMember)
	control, err := tasks.CreateRunControlFor(t.Context(), stale.ID, model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlRetry, ExpectedVersion: stale.Version}, member)
	if err != nil {
		t.Fatal(err)
	}
	request := model.ReviewRequeueRequest{TargetRunID: started.Run.ID, ExpectedVersion: stale.Version, ReviewNote: "Reviewed the partial work and remaining checklist"}
	if _, err := tasks.ReviewAndRequeueFor(t.Context(), stale.ID, request, member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member recovery error = %v, want forbidden", err)
	}
	if _, err := tasks.ReviewAndRequeueFor(t.Context(), stale.ID, request, agent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent recovery error = %v, want forbidden", err)
	}
	operator := HumanPrincipalWithRole("human:operator", RoleAdmin)
	recovered, err := tasks.ReviewAndRequeueFor(t.Context(), stale.ID, request, operator)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Task.Status != model.TaskQueued || recovered.Task.Owner != "" || recovered.Task.Version != stale.Version+1 {
		t.Fatalf("requeued task = %+v", recovered.Task)
	}
	if recovered.Task.CreatedBy != started.Task.CreatedBy || recovered.Task.LastEditedBy != operator.ID || recovered.Task.CurrentNote != request.ReviewNote {
		t.Fatalf("requeue provenance = creator %q editor %q note %q", recovered.Task.CreatedBy, recovered.Task.LastEditedBy, recovered.Task.CurrentNote)
	}
	if recovered.Control.ID != control.ID || recovered.Control.Status != model.RunControlCompleted || recovered.Control.CompletedAt == nil {
		t.Fatalf("completed recovery control = %+v", recovered.Control)
	}
	if len(recovered.Task.Runs) != 1 || recovered.Task.Runs[0].Status != model.TaskStale || recovered.Task.Runs[0].EndedAt == nil {
		t.Fatalf("stale run history changed = %+v", recovered.Task.Runs)
	}
	var events int
	if err := tasks.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE task_id=? AND kind='task.reviewed_requeued' AND actor=?`, stale.ID, operator.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("recovery events = %d, %v", events, err)
	}
	replacement := AgentPrincipal("agent:replacement")
	claimed, err := tasks.ClaimFor(t.Context(), stale.ID, model.ClaimRequest{ExpectedVersion: recovered.Task.Version, IdempotencyKey: "review-requeue-claim"}, replacement)
	if err != nil || claimed.Run.ID == started.Run.ID {
		t.Fatalf("fresh claim = %+v, %v", claimed, err)
	}
}

func TestControlCompletionConflictsAfterReplacementAndDoesNotTransfer(t *testing.T) {
	tasks := testService(t, -time.Second)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Replacement race", Checklist: []string{"Work"}, IdempotencyKey: "race-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.SweepStale(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale, _ := tasks.GetFor(t.Context(), started.Task.ID, agent)
	control, err := tasks.CreateRunControlFor(t.Context(), stale.ID, model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlRetry, ExpectedVersion: stale.Version}, HumanPrincipal("human:operator"))
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeAndAcceptControl(t, tasks, agent, control.ID)
	replacement, err := tasks.ClaimFor(t.Context(), stale.ID, model.ClaimRequest{ExpectedVersion: stale.Version, IdempotencyKey: "race-replacement"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := tasks.HeartbeatFor(t.Context(), stale.ID, replacement.Run.ID, agent)
	if err != nil || len(heartbeat.PendingControls) != 0 {
		t.Fatalf("replacement heartbeat controls = %+v, %v", heartbeat.PendingControls, err)
	}
	if _, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlCompleted, ExpectedVersion: stale.Version, IdempotencyKey: "race-complete"}, agent); !errors.Is(err, ErrConflict) {
		t.Fatalf("completion error = %v, want conflict", err)
	}
	current, _ := tasks.GetFor(t.Context(), stale.ID, agent)
	if current.Status != model.TaskActive || current.Runs[0].ID != replacement.Run.ID {
		t.Fatalf("replacement was overwritten: %+v", current)
	}
}

func TestCancelControlCannotOverwriteTerminalTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Terminal race", Checklist: []string{"Finish"}, IdempotencyKey: "terminal-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	control, err := tasks.CreateRunControlFor(t.Context(), started.Task.ID, model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlCancel, ExpectedVersion: started.Task.Version}, HumanPrincipal("human:operator"))
	if err != nil {
		t.Fatal(err)
	}
	acknowledgeAndAcceptControl(t, tasks, agent, control.ID)
	done, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{started.Task.Items[0].ID}, IdempotencyKey: "terminal-done"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlCompleted, ExpectedVersion: started.Task.Version, IdempotencyKey: "terminal-cancel-complete"}, agent); !errors.Is(err, ErrConflict) {
		t.Fatalf("completion error = %v, want conflict", err)
	}
	current, _ := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if current.Status != model.TaskDone || current.Version != done.Version {
		t.Fatalf("terminal task was overwritten: %+v", current)
	}
}

func acknowledgeAndAcceptControl(t *testing.T, tasks *Service, agent Principal, controlID string) {
	t.Helper()
	if _, err := tasks.UpdateRunControlFor(t.Context(), controlID, model.UpdateRunControlRequest{Status: model.RunControlAcknowledged, IdempotencyKey: controlID + "-ack"}, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateRunControlFor(t.Context(), controlID, model.UpdateRunControlRequest{Status: model.RunControlAccepted, IdempotencyKey: controlID + "-accept"}, agent); err != nil {
		t.Fatal(err)
	}
}

func TestRunControlRejectRequiresReasonAndDoesNotMutateTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Cancel safely", Checklist: []string{"Work"}, IdempotencyKey: "reject-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	control, err := tasks.CreateRunControlFor(t.Context(), started.Task.ID, model.CreateRunControlRequest{TargetRunID: started.Run.ID, Kind: model.RunControlCancel, ExpectedVersion: started.Task.Version}, HumanPrincipal("human:operator"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlAcknowledged, IdempotencyKey: "cancel-ack"}, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlRejected, IdempotencyKey: "cancel-reject-empty"}, agent); err == nil {
		t.Fatal("empty rejection note was accepted")
	}
	rejected, err := tasks.UpdateRunControlFor(t.Context(), control.ID, model.UpdateRunControlRequest{Status: model.RunControlRejected, OutcomeNote: "Critical operation cannot be interrupted", IdempotencyKey: "cancel-reject"}, agent)
	if err != nil || rejected.Status != model.RunControlRejected {
		t.Fatalf("rejected control = %+v, %v", rejected, err)
	}
	current, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil || current.Status != model.TaskActive || current.Runs[0].EndedAt != nil {
		t.Fatalf("task after rejection = %+v, %v", current, err)
	}
}
