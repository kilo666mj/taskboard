package service

import (
	"errors"
	"github.com/kilo666mj/taskboard/internal/model"
	"testing"
	"time"
)

func TestTrustedSessionBridgeRequestLifecycleAndIsolation(t *testing.T) {
	tasks := testService(t, time.Minute)
	controller := AgentPrincipal("agent:controller")
	human := HumanPrincipal("human:owner")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Session", Checklist: []string{"Work"}, IdempotencyKey: "session-start"}, controller)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := tasks.RegisterSessionBridgeFor(t.Context(), started.Task.ID, model.RegisterSessionBridgeRequest{RunID: started.Run.ID, State: model.SessionBridgeAvailable, Label: "Controller session", CanOpen: true, CanResume: true, ExpiresAt: time.Now().UTC().Add(time.Hour)}, controller)
	if err != nil || bridge.Controller != controller.ID {
		t.Fatalf("bridge = %+v, %v", bridge, err)
	}
	request, err := tasks.CreateSessionBridgeRequestFor(t.Context(), started.Task.ID, model.CreateSessionBridgeRequest{RunID: started.Run.ID, Action: model.SessionActionOpen}, human)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := tasks.ListSessionBridgeRequestsFor(t.Context(), controller)
	if err != nil || len(pending) != 1 || pending[0].ID != request.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if _, err = tasks.UpdateSessionBridgeRequestFor(t.Context(), request.ID, model.UpdateSessionBridgeRequest{Status: model.SessionRequestAcknowledged}, AgentPrincipal("agent:other")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross controller error = %v", err)
	}
	ack, err := tasks.UpdateSessionBridgeRequestFor(t.Context(), request.ID, model.UpdateSessionBridgeRequest{Status: model.SessionRequestAcknowledged}, controller)
	if err != nil || ack.Status != model.SessionRequestAcknowledged {
		t.Fatalf("ack = %+v, %v", ack, err)
	}
	done, err := tasks.UpdateSessionBridgeRequestFor(t.Context(), request.ID, model.UpdateSessionBridgeRequest{Status: model.SessionRequestCompleted, OutcomeNote: "Focused trusted session"}, controller)
	if err != nil || done.Status != model.SessionRequestCompleted {
		t.Fatalf("done = %+v, %v", done, err)
	}
}
