package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestTaskEscalationStorageLifecycle(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,title,status,version,created_at,updated_at) VALUES(?,?,?,1,?,?)`, "task-1", "Escalation task", model.TaskActive, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO agent_runs(id,task_id,agent,callsign,status,lease_expires_at,last_heartbeat_at,started_at) VALUES(?,?,?,?,?,?,?,?)`, "run-1", "task-1", "agent:worker", "Maple", model.TaskActive, formatTime(now.Add(time.Hour)), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	tx, err := database.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	question := model.TaskMessage{ID: "question-1", TaskID: "task-1", Author: "agent:worker", AuthorRunID: "run-1", Kind: model.MessageQuestion, Body: "Which path?", CreatedAt: now}
	if err := InsertTaskMessage(t.Context(), tx, question); err != nil {
		t.Fatal(err)
	}
	escalation := model.TaskEscalation{ID: "escalation-1", TaskID: "task-1", RunID: "run-1", QuestionMessageID: question.ID, Blocking: true, Options: []string{"A", "B"}, Recommendation: "A", Status: model.EscalationOpen, CreatedAt: now}
	if err := InsertTaskEscalation(t.Context(), tx, escalation); err != nil {
		t.Fatal(err)
	}
	answer := model.TaskMessage{ID: "answer-1", TaskID: "task-1", Author: "human:operator", Kind: model.MessageAnswer, Body: "Use A", ReplyToID: question.ID, CreatedAt: now.Add(time.Second)}
	if err := InsertTaskMessage(t.Context(), tx, answer); err != nil {
		t.Fatal(err)
	}
	if err := ResolveTaskEscalation(t.Context(), tx, escalation.ID, answer.ID, "A", answer.Author, answer.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetTaskEscalation(t.Context(), escalation.ID)
	if err != nil || loaded.Status != model.EscalationAnswered || loaded.AnswerMessageID != answer.ID || loaded.SelectedOption != "A" || loaded.ResolvedAt == nil || len(loaded.Options) != 2 {
		t.Fatalf("loaded escalation = %+v, %v", loaded, err)
	}
	items, err := database.ListTaskEscalations(t.Context(), "task-1")
	if err != nil || len(items) != 1 {
		t.Fatalf("listed escalations = %+v, %v", items, err)
	}
}
