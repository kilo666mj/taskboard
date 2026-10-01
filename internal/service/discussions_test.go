package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func TestLiveDiscussionLifecycleLeavesTheTaskUntouched(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, escalation := escalateForDecision(t, tasks, "discuss", model.CreateEscalationRequest{Blocking: true, Answerers: []string{"human:approver"}})
	controller, person := AgentPrincipal("agent:worker"), HumanPrincipal("human:approver")

	marked := []model.Task{task}
	tasks.MarkDiscussableFor(t.Context(), marked, person)
	if marked[0].Discussable {
		t.Fatal("task is discussable before its controller advertises discussions")
	}
	if _, err := tasks.StartDiscussionFor(t.Context(), task.ID, model.StartDiscussionRequest{}, person); !errors.Is(err, ErrValidation) {
		t.Fatalf("start without advertisement error = %v", err)
	}
	if _, err := tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{DiscussionCapability}, TTLSeconds: 120}, controller); err != nil {
		t.Fatal(err)
	}
	tasks.MarkDiscussableFor(t.Context(), marked, person)
	if !marked[0].Discussable {
		t.Fatal("task is not discussable after advertisement")
	}

	started, err := tasks.StartDiscussionFor(t.Context(), task.ID, model.StartDiscussionRequest{Message: "Why this host?"}, person)
	if err != nil || started.Status != model.DiscussionRequested || started.Controller != "agent:worker" || len(started.Messages) != 1 {
		t.Fatalf("started = %+v, %v", started, err)
	}
	again, err := tasks.StartDiscussionFor(t.Context(), task.ID, model.StartDiscussionRequest{}, person)
	if err != nil || again.ID != started.ID {
		t.Fatalf("second start = %+v, %v", again, err)
	}
	listed, err := tasks.ListControllerDiscussionsFor(t.Context(), controller)
	if err != nil || len(listed) != 1 || listed[0].ID != started.ID {
		t.Fatalf("controller list = %+v, %v", listed, err)
	}
	if _, err := tasks.AddDiscussionMessageFor(t.Context(), started.ID, model.DiscussionMessageRequest{Body: "Too early"}, controller); !errors.Is(err, ErrValidation) {
		t.Fatalf("reply before accept error = %v", err)
	}
	if _, err := tasks.UpdateDiscussionFor(t.Context(), started.ID, model.UpdateDiscussionRequest{Status: model.DiscussionActive, AgentStatus: "thinking"}, controller); err != nil {
		t.Fatal(err)
	}
	reply, err := tasks.AddDiscussionMessageFor(t.Context(), started.ID, model.DiscussionMessageRequest{Body: "It hosts the backup job.", IdempotencyKey: "reply-one"}, controller)
	if err != nil || reply.Role != "agent" || reply.Author != "agent:worker" {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	replayed, err := tasks.AddDiscussionMessageFor(t.Context(), started.ID, model.DiscussionMessageRequest{Body: "It hosts the backup job.", IdempotencyKey: "reply-one"}, controller)
	if err != nil || replayed.ID != reply.ID {
		t.Fatalf("replayed reply = %+v, %v", replayed, err)
	}
	if _, err := tasks.AddDiscussionMessageFor(t.Context(), started.ID, model.DiscussionMessageRequest{Body: "Thanks"}, person); err != nil {
		t.Fatal(err)
	}
	newer, err := tasks.GetDiscussionFor(t.Context(), started.ID, reply.ID, controller)
	if err != nil || newer.Status != model.DiscussionActive || newer.AgentStatus != "ready" || len(newer.Messages) != 1 || newer.Messages[0].Body != "Thanks" {
		t.Fatalf("messages after reply = %+v, %v", newer, err)
	}

	if _, err := tasks.GetDiscussionFor(t.Context(), started.ID, "", AgentPrincipal("agent:other")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other controller error = %v", err)
	}
	if _, err := tasks.AddDiscussionMessageFor(t.Context(), started.ID, model.DiscussionMessageRequest{Body: "Hi"}, HumanPrincipalWithRole("human:viewer", RoleViewer)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer message error = %v", err)
	}
	if _, err := tasks.UpdateDiscussionFor(t.Context(), started.ID, model.UpdateDiscussionRequest{Status: model.DiscussionActive}, person); !errors.Is(err, ErrForbidden) {
		t.Fatalf("person accept error = %v", err)
	}

	ended, err := tasks.UpdateDiscussionFor(t.Context(), started.ID, model.UpdateDiscussionRequest{Status: model.DiscussionEnded}, person)
	if err != nil || ended.Status != model.DiscussionEnded || ended.EndedAt == nil {
		t.Fatalf("ended = %+v, %v", ended, err)
	}
	if _, err := tasks.AddDiscussionMessageFor(t.Context(), started.ID, model.DiscussionMessageRequest{Body: "Late"}, person); !errors.Is(err, ErrConflict) {
		t.Fatalf("message after end error = %v", err)
	}

	after, err := tasks.GetFor(t.Context(), task.ID, person)
	if err != nil || after.Status != model.TaskWaiting || after.Version != task.Version {
		t.Fatalf("task after discussion = %+v, %v", after, err)
	}
	open, err := tasks.ListEscalationsFor(t.Context(), task.ID, person)
	if err != nil || open[0].ID != escalation.ID || open[0].Status != model.EscalationOpen {
		t.Fatalf("escalation after discussion = %+v, %v", open, err)
	}
	history, err := tasks.ListTaskDiscussionsFor(t.Context(), task.ID, person)
	if err != nil || len(history) != 1 || len(history[0].Messages) != 3 {
		t.Fatalf("history = %+v, %v", history, err)
	}
}

func TestIdleDiscussionsEnd(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, _ := escalateForDecision(t, tasks, "idle", model.CreateEscalationRequest{Blocking: true})
	controller, person := AgentPrincipal("agent:worker"), HumanPrincipal("human:approver")
	if _, err := tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{DiscussionCapability}, TTLSeconds: 120}, controller); err != nil {
		t.Fatal(err)
	}
	started, err := tasks.StartDiscussionFor(t.Context(), task.ID, model.StartDiscussionRequest{}, person)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-DiscussionIdle - time.Minute).Format(time.RFC3339Nano)
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE task_discussions SET last_activity_at=? WHERE id=?`, past, started.ID); err != nil {
		t.Fatal(err)
	}
	if listed, err := tasks.ListControllerDiscussionsFor(t.Context(), controller); err != nil || len(listed) != 0 {
		t.Fatalf("idle discussion still listed = %+v, %v", listed, err)
	}
	history, err := tasks.ListTaskDiscussionsFor(t.Context(), task.ID, person)
	if err != nil || history[0].Status != model.DiscussionEnded || history[0].EndReason != "idle" {
		t.Fatalf("idle discussion = %+v, %v", history, err)
	}
	fresh, err := tasks.StartDiscussionFor(t.Context(), task.ID, model.StartDiscussionRequest{}, person)
	if err != nil || fresh.ID == started.ID {
		t.Fatalf("new discussion after idle = %+v, %v", fresh, err)
	}
}
