package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestAgentCredentialRotationRevocationAndOffboarding(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	credential, token, err := database.CreateAgentCredential(t.Context(), "Build agent", "agent:build", nil, "owner@example.com")
	if err != nil || token == "" {
		t.Fatalf("create credential = %+v, token=%q, err=%v", credential, token, err)
	}
	if principal, valid, err := database.AuthenticateAgentCredential(t.Context(), token); err != nil || !valid || principal != "agent:build" {
		t.Fatalf("authenticate = %q/%v/%v", principal, valid, err)
	}
	_, rotated, err := database.RotateAgentCredential(t.Context(), credential.ID, "owner@example.com")
	if err != nil || rotated == token {
		t.Fatalf("rotate token=%q err=%v", rotated, err)
	}
	if _, valid, _ := database.AuthenticateAgentCredential(t.Context(), token); valid {
		t.Fatal("old token remained valid after rotation")
	}
	if _, valid, _ := database.AuthenticateAgentCredential(t.Context(), rotated); !valid {
		t.Fatal("rotated token is not valid")
	}
	if err := database.OffboardPrincipal(t.Context(), "agent:build", "owner@example.com", "service retired"); err != nil {
		t.Fatal(err)
	}
	if revoked, err := database.PrincipalRevoked(t.Context(), "agent:build"); err != nil || !revoked {
		t.Fatalf("revoked = %v, %v", revoked, err)
	}
	if _, valid, _ := database.AuthenticateAgentCredential(t.Context(), rotated); valid {
		t.Fatal("offboarded credential remained valid")
	}
}

func TestAdministrativeAuditRetentionAndWebhookQueue(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	_, err = database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,title,status,version,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?)`, "old-task", "Old", model.TaskDone, 1, formatTime(now.AddDate(0, 0, -40)), formatTime(now), formatTime(now.AddDate(0, 0, -35)))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InsertAdminAudit(t.Context(), "owner", "retention.test", "workspace", map[string]any{"days": 30}); err != nil {
		t.Fatal(err)
	}
	if count, err := database.ApplyRetention(t.Context(), now.AddDate(0, 0, -30), "owner@example.com"); err != nil || count != 1 {
		t.Fatalf("retention count/error = %d/%v", count, err)
	}
	audit, err := database.ListAdminAudit(t.Context(), 10)
	if err != nil || len(audit) != 2 || audit[0].Action != "retention.applied" || audit[1].Action != "retention.test" {
		t.Fatalf("audit = %+v, %v", audit, err)
	}
	if _, err := database.DB().ExecContext(t.Context(), `UPDATE admin_audit SET action='tampered' WHERE id=?`, audit[1].ID); err == nil {
		t.Fatal("administrative audit update was accepted")
	}
	if _, err := database.DB().ExecContext(t.Context(), `DELETE FROM admin_audit WHERE id=?`, audit[1].ID); err == nil {
		t.Fatal("administrative audit deletion was accepted")
	}

	_, err = database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,title,status,version,created_at,updated_at) VALUES(?,?,?,?,?,?)`, "event-task", "Event", model.TaskQueued, 1, formatTime(now), formatTime(now))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.DB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	event := model.Event{ID: "01M2ADMINWEBHOOKEVENT000000", TaskID: "event-task", Kind: "task.created", Actor: "owner", Payload: map[string]any{"status": "queued"}, CreatedAt: now}
	if err := InsertEvent(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if queued, err := database.QueueWebhookEvents(t.Context(), 10); err != nil || queued != 1 {
		t.Fatalf("queued = %d, %v", queued, err)
	}
	delivery, loaded, err := database.NextWebhookDelivery(t.Context())
	if err != nil || loaded.ID != event.ID {
		t.Fatalf("delivery/event = %+v/%+v/%v", delivery, loaded, err)
	}
	if err := database.FailWebhookDelivery(t.Context(), delivery.ID, 1, 1, now, "failed"); err != nil {
		t.Fatal(err)
	}
	dead, err := database.ListDeadWebhookDeliveries(t.Context(), 10)
	if err != nil || len(dead) != 1 {
		t.Fatalf("dead letters = %+v, %v", dead, err)
	}
	if err := database.RetryWebhookDelivery(t.Context(), delivery.ID, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestAdministrativeMutationRollsBackWithoutAuditActor(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if _, _, err := database.CreateAgentCredential(t.Context(), "Build agent", "agent:build", nil, ""); err == nil {
		t.Fatal("credential creation without an audit actor succeeded")
	}
	credentials, err := database.ListAgentCredentials(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 0 {
		t.Fatalf("credentials after rolled-back creation = %+v", credentials)
	}
}
