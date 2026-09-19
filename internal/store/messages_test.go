package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestTaskMessagePendingReceiptLifecycle(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,title,status,version,created_at,updated_at) VALUES(?,?,?,1,?,?)`, "task-1", "Message task", model.TaskActive, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-1", "run-2"} {
		if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO agent_runs(id,task_id,agent,callsign,status,lease_expires_at,last_heartbeat_at,started_at) VALUES(?,?,?,?,?,?,?,?)`, runID, "task-1", "agent:worker", runID, model.TaskActive, formatTime(now.Add(time.Hour)), formatTime(now), formatTime(now)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []model.TaskMessage{
		{ID: "message-task", TaskID: "task-1", Author: "human:operator", Kind: model.MessageInstruction, Body: "Run the focused tests", RequiresAck: true, CreatedAt: now},
		{ID: "message-run", TaskID: "task-1", Author: "human:operator", TargetRunID: "run-1", Kind: model.MessageNote, Body: "Only for the first run", CreatedAt: now.Add(time.Second)},
	} {
		if err := InsertTaskMessage(t.Context(), tx, message); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	pending, err := database.ListPendingTaskMessages(t.Context(), "task-1", "run-1", "agent:worker", 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending messages = %+v, %v", pending, err)
	}
	receiptTx, err := database.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordMessageReceipt(t.Context(), receiptTx, "message-task", "run-1", "agent:worker", false, now.Add(2*time.Second)); err != nil {
		_ = receiptTx.Rollback()
		t.Fatal(err)
	}
	if err := RecordMessageReceipt(t.Context(), receiptTx, "message-run", "run-1", "agent:worker", false, now.Add(2*time.Second)); err != nil {
		_ = receiptTx.Rollback()
		t.Fatal(err)
	}
	if err := receiptTx.Commit(); err != nil {
		t.Fatal(err)
	}
	pending, err = database.ListPendingTaskMessages(t.Context(), "task-1", "run-1", "agent:worker", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "message-task" {
		t.Fatalf("pending after observation = %+v, %v", pending, err)
	}

	receiptTx, err = database.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordMessageReceipt(t.Context(), receiptTx, "message-task", "run-1", "agent:worker", true, now.Add(3*time.Second)); err != nil {
		_ = receiptTx.Rollback()
		t.Fatal(err)
	}
	if err := receiptTx.Commit(); err != nil {
		t.Fatal(err)
	}
	pending, err = database.ListPendingTaskMessages(t.Context(), "task-1", "run-1", "agent:worker", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after acknowledgement = %+v, %v", pending, err)
	}
	pending, err = database.ListPendingTaskMessages(t.Context(), "task-1", "run-2", "agent:worker", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "message-task" {
		t.Fatalf("replacement run pending = %+v, %v", pending, err)
	}

	messages, err := database.ListTaskMessages(t.Context(), "task-1", "", 10)
	if err != nil || len(messages) != 2 {
		t.Fatalf("message thread = %+v, %v", messages, err)
	}
	var instruction model.TaskMessage
	for _, message := range messages {
		if message.ID == "message-task" {
			instruction = message
		}
	}
	if len(instruction.Receipts) != 1 || instruction.Receipts[0].AcknowledgedAt == nil {
		t.Fatalf("instruction receipts = %+v", instruction.Receipts)
	}
	exported, err := database.ListAllTaskMessages(t.Context())
	if err != nil || len(exported) != 2 {
		t.Fatalf("exported messages = %+v, %v", exported, err)
	}
}
