package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/agentidentity"
)

const latestSchemaVersion = 16

type schemaMigration struct {
	Version  int
	Name     string
	SQLite   []string
	Postgres []string
}

type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

var schemaMigrations = []schemaMigration{
	{
		Version:  1,
		Name:     "baseline",
		SQLite:   sqliteBaselineStatements(),
		Postgres: postgresBaselineStatements(),
	},
	{
		Version: 2,
		Name:    "postgres_event_notifications",
		SQLite:  []string{`SELECT 1`},
		Postgres: []string{
			`CREATE OR REPLACE FUNCTION taskboard_notify_event() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				PERFORM pg_notify('taskboard_events', NEW.id);
				RETURN NEW;
			END;
			$$`,
			`DROP TRIGGER IF EXISTS taskboard_event_notify ON events`,
			`CREATE TRIGGER taskboard_event_notify AFTER INSERT ON events FOR EACH ROW EXECUTE FUNCTION taskboard_notify_event()`,
		},
	},
	{
		Version: 3,
		Name:    "administrative_lifecycle",
		SQLite: []string{
			`CREATE TABLE agent_credentials (id TEXT PRIMARY KEY,name TEXT NOT NULL,principal_id TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,expires_at TEXT,revoked_at TEXT,last_used_at TEXT)`,
			`CREATE INDEX idx_agent_credentials_principal ON agent_credentials(principal_id,revoked_at)`,
			`CREATE TABLE revoked_principals (principal_id TEXT PRIMARY KEY,reason TEXT NOT NULL DEFAULT '',revoked_by TEXT NOT NULL,revoked_at TEXT NOT NULL)`,
			`CREATE TABLE admin_audit (id TEXT PRIMARY KEY,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL DEFAULT '',detail TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_admin_audit_created ON admin_audit(created_at,id)`,
			`CREATE TRIGGER admin_audit_no_update BEFORE UPDATE ON admin_audit BEGIN SELECT RAISE(ABORT, 'admin audit is append-only'); END`,
			`CREATE TRIGGER admin_audit_no_delete BEFORE DELETE ON admin_audit BEGIN SELECT RAISE(ABORT, 'admin audit is append-only'); END`,
			`CREATE TABLE webhook_deliveries (id TEXT PRIMARY KEY,event_id TEXT NOT NULL UNIQUE REFERENCES events(id) ON DELETE CASCADE,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at TEXT NOT NULL,last_error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
			`CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries(status,next_attempt_at)`,
		},
		Postgres: []string{
			`CREATE TABLE agent_credentials (id TEXT PRIMARY KEY,name TEXT NOT NULL,principal_id TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,expires_at TEXT,revoked_at TEXT,last_used_at TEXT)`,
			`CREATE INDEX idx_agent_credentials_principal ON agent_credentials(principal_id,revoked_at)`,
			`CREATE TABLE revoked_principals (principal_id TEXT PRIMARY KEY,reason TEXT NOT NULL DEFAULT '',revoked_by TEXT NOT NULL,revoked_at TEXT NOT NULL)`,
			`CREATE TABLE admin_audit (id TEXT PRIMARY KEY,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL DEFAULT '',detail TEXT NOT NULL DEFAULT '{}',created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_admin_audit_created ON admin_audit(created_at,id)`,
			`CREATE FUNCTION taskboard_protect_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'admin audit is append-only'; END; $$`,
			`CREATE TRIGGER admin_audit_no_update BEFORE UPDATE ON admin_audit FOR EACH ROW EXECUTE FUNCTION taskboard_protect_admin_audit()`,
			`CREATE TRIGGER admin_audit_no_delete BEFORE DELETE ON admin_audit FOR EACH ROW EXECUTE FUNCTION taskboard_protect_admin_audit()`,
			`CREATE TABLE webhook_deliveries (id TEXT PRIMARY KEY,event_id TEXT NOT NULL UNIQUE REFERENCES events(id) ON DELETE CASCADE,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at TEXT NOT NULL,last_error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
			`CREATE INDEX idx_webhook_deliveries_due ON webhook_deliveries(status,next_attempt_at)`,
		},
	},
	{
		Version: 4,
		Name:    "agent_run_callsigns",
		SQLite: []string{
			`ALTER TABLE agent_runs ADD COLUMN callsign TEXT NOT NULL DEFAULT ''`,
			`CREATE UNIQUE INDEX idx_runs_active_callsign ON agent_runs(lower(callsign)) WHERE ended_at IS NULL AND status='active' AND callsign<>''`,
		},
		Postgres: []string{
			`ALTER TABLE agent_runs ADD COLUMN callsign TEXT NOT NULL DEFAULT ''`,
			`CREATE UNIQUE INDEX idx_runs_active_callsign ON agent_runs(lower(callsign)) WHERE ended_at IS NULL AND status='active' AND callsign<>''`,
		},
	},
	{
		Version: 5,
		Name:    "task_messages",
		SQLite: []string{
			`CREATE TABLE task_messages (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,author TEXT NOT NULL,author_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,target_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,kind TEXT NOT NULL,body TEXT NOT NULL,reply_to_id TEXT REFERENCES task_messages(id) ON DELETE SET NULL,supersedes_id TEXT REFERENCES task_messages(id) ON DELETE SET NULL,requires_ack INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_task_messages_task ON task_messages(task_id,id DESC)`,
			`CREATE INDEX idx_task_messages_target ON task_messages(target_run_id,id DESC)`,
			`CREATE UNIQUE INDEX idx_task_messages_supersedes ON task_messages(supersedes_id) WHERE supersedes_id IS NOT NULL`,
			`CREATE TABLE message_receipts (message_id TEXT NOT NULL REFERENCES task_messages(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,observer TEXT NOT NULL,observed_at TEXT NOT NULL,acknowledged_at TEXT,PRIMARY KEY(message_id,run_id))`,
			`CREATE INDEX idx_message_receipts_run ON message_receipts(run_id,message_id)`,
		},
		Postgres: []string{
			`CREATE TABLE task_messages (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,author TEXT NOT NULL,author_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,target_run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,kind TEXT NOT NULL,body TEXT NOT NULL,reply_to_id TEXT REFERENCES task_messages(id) ON DELETE SET NULL,supersedes_id TEXT REFERENCES task_messages(id) ON DELETE SET NULL,requires_ack BOOLEAN NOT NULL DEFAULT FALSE,created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_task_messages_task ON task_messages(task_id,id DESC)`,
			`CREATE INDEX idx_task_messages_target ON task_messages(target_run_id,id DESC)`,
			`CREATE UNIQUE INDEX idx_task_messages_supersedes ON task_messages(supersedes_id) WHERE supersedes_id IS NOT NULL`,
			`CREATE TABLE message_receipts (message_id TEXT NOT NULL REFERENCES task_messages(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,observer TEXT NOT NULL,observed_at TEXT NOT NULL,acknowledged_at TEXT,PRIMARY KEY(message_id,run_id))`,
			`CREATE INDEX idx_message_receipts_run ON message_receipts(run_id,message_id)`,
		},
	},
	{
		Version: 6,
		Name:    "task_escalations",
		SQLite: []string{
			`CREATE TABLE task_escalations (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,question_message_id TEXT NOT NULL UNIQUE REFERENCES task_messages(id) ON DELETE CASCADE,answer_message_id TEXT UNIQUE REFERENCES task_messages(id) ON DELETE SET NULL,blocking INTEGER NOT NULL DEFAULT 0,options_json TEXT NOT NULL DEFAULT '[]',recommendation TEXT NOT NULL DEFAULT '',selected_option TEXT NOT NULL DEFAULT '',status TEXT NOT NULL, resolved_by TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,resolved_at TEXT)`,
			`CREATE INDEX idx_task_escalations_task ON task_escalations(task_id,id DESC)`,
			`CREATE INDEX idx_task_escalations_open ON task_escalations(task_id,status,id)`,
		},
		Postgres: []string{
			`CREATE TABLE task_escalations (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,question_message_id TEXT NOT NULL UNIQUE REFERENCES task_messages(id) ON DELETE CASCADE,answer_message_id TEXT UNIQUE REFERENCES task_messages(id) ON DELETE SET NULL,blocking BOOLEAN NOT NULL DEFAULT FALSE,options_json TEXT NOT NULL DEFAULT '[]',recommendation TEXT NOT NULL DEFAULT '',selected_option TEXT NOT NULL DEFAULT '',status TEXT NOT NULL, resolved_by TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,resolved_at TEXT)`,
			`CREATE INDEX idx_task_escalations_task ON task_escalations(task_id,id DESC)`,
			`CREATE INDEX idx_task_escalations_open ON task_escalations(task_id,status,id)`,
		},
	},
	{
		Version: 7,
		Name:    "run_control_requests",
		SQLite: []string{
			`CREATE TABLE run_control_requests (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,target_run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,target_agent TEXT NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL,requested_by TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',outcome_note TEXT NOT NULL DEFAULT '',task_version INTEGER NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,expires_at TEXT NOT NULL,acknowledged_at TEXT,decided_at TEXT,completed_at TEXT)`,
			`CREATE INDEX idx_run_controls_task ON run_control_requests(task_id,id DESC)`,
			`CREATE INDEX idx_run_controls_agent_status ON run_control_requests(target_agent,status,expires_at,id)`,
		},
		Postgres: []string{
			`CREATE TABLE run_control_requests (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,target_run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,target_agent TEXT NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL,requested_by TEXT NOT NULL,reason TEXT NOT NULL DEFAULT '',outcome_note TEXT NOT NULL DEFAULT '',task_version BIGINT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,expires_at TEXT NOT NULL,acknowledged_at TEXT,decided_at TEXT,completed_at TEXT)`,
			`CREATE INDEX idx_run_controls_task ON run_control_requests(task_id,id DESC)`,
			`CREATE INDEX idx_run_controls_agent_status ON run_control_requests(target_agent,status,expires_at,id)`,
		},
	},
	{
		Version: 8,
		Name:    "task_references",
		SQLite: []string{
			`CREATE TABLE task_references (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,kind TEXT NOT NULL,label TEXT NOT NULL,locator TEXT NOT NULL DEFAULT '',url TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,provenance TEXT NOT NULL,created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_task_references_task ON task_references(task_id,id)`,
			`CREATE INDEX idx_task_references_run ON task_references(run_id,id)`,
		},
		Postgres: []string{
			`CREATE TABLE task_references (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,kind TEXT NOT NULL,label TEXT NOT NULL,locator TEXT NOT NULL DEFAULT '',url TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,provenance TEXT NOT NULL,created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_task_references_task ON task_references(task_id,id)`,
			`CREATE INDEX idx_task_references_run ON task_references(run_id,id)`,
		},
	},
	{
		Version: 9,
		Name:    "run_handoffs",
		SQLite: []string{
			`CREATE TABLE run_handoffs (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,kind TEXT NOT NULL,last_completed_step TEXT NOT NULL DEFAULT '',worktree TEXT NOT NULL DEFAULT '',branch TEXT NOT NULL DEFAULT '',commits_json TEXT NOT NULL DEFAULT '[]',pull_requests_json TEXT NOT NULL DEFAULT '[]',validation_json TEXT NOT NULL DEFAULT '[]',review_findings_json TEXT NOT NULL DEFAULT '[]',blocker TEXT NOT NULL DEFAULT '',next_action TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_run_handoffs_task ON run_handoffs(task_id,id DESC)`,
			`CREATE INDEX idx_run_handoffs_run ON run_handoffs(run_id,id DESC)`,
		},
		Postgres: []string{
			`CREATE TABLE run_handoffs (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,kind TEXT NOT NULL,last_completed_step TEXT NOT NULL DEFAULT '',worktree TEXT NOT NULL DEFAULT '',branch TEXT NOT NULL DEFAULT '',commits_json TEXT NOT NULL DEFAULT '[]',pull_requests_json TEXT NOT NULL DEFAULT '[]',validation_json TEXT NOT NULL DEFAULT '[]',review_findings_json TEXT NOT NULL DEFAULT '[]',blocker TEXT NOT NULL DEFAULT '',next_action TEXT NOT NULL DEFAULT '',created_by TEXT NOT NULL,created_at TEXT NOT NULL)`,
			`CREATE INDEX idx_run_handoffs_task ON run_handoffs(task_id,id DESC)`,
			`CREATE INDEX idx_run_handoffs_run ON run_handoffs(run_id,id DESC)`,
		},
	},
	{
		Version: 10,
		Name:    "completion_contracts",
		SQLite: []string{
			`CREATE TABLE completion_requirements (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,kind TEXT NOT NULL,label TEXT NOT NULL,required INTEGER NOT NULL DEFAULT 1,status TEXT NOT NULL,created_by TEXT NOT NULL,verified_by TEXT NOT NULL DEFAULT '',waiver_reason TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,verified_at TEXT)`,
			`CREATE INDEX idx_completion_requirements_task ON completion_requirements(task_id,id)`,
			`CREATE TABLE completion_evidence (id TEXT PRIMARY KEY,requirement_id TEXT NOT NULL REFERENCES completion_requirements(id) ON DELETE CASCADE,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,reference_id TEXT REFERENCES task_references(id) ON DELETE SET NULL,note TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,submitted_by TEXT NOT NULL,reviewed_by TEXT NOT NULL DEFAULT '',review_note TEXT NOT NULL DEFAULT '',submitted_at TEXT NOT NULL,reviewed_at TEXT)`,
			`CREATE INDEX idx_completion_evidence_requirement ON completion_evidence(requirement_id,id)`,
		},
		Postgres: []string{
			`CREATE TABLE completion_requirements (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,kind TEXT NOT NULL,label TEXT NOT NULL,required BOOLEAN NOT NULL DEFAULT TRUE,status TEXT NOT NULL,created_by TEXT NOT NULL,verified_by TEXT NOT NULL DEFAULT '',waiver_reason TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,verified_at TEXT)`,
			`CREATE INDEX idx_completion_requirements_task ON completion_requirements(task_id,id)`,
			`CREATE TABLE completion_evidence (id TEXT PRIMARY KEY,requirement_id TEXT NOT NULL REFERENCES completion_requirements(id) ON DELETE CASCADE,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT REFERENCES agent_runs(id) ON DELETE SET NULL,reference_id TEXT REFERENCES task_references(id) ON DELETE SET NULL,note TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,submitted_by TEXT NOT NULL,reviewed_by TEXT NOT NULL DEFAULT '',review_note TEXT NOT NULL DEFAULT '',submitted_at TEXT NOT NULL,reviewed_at TEXT)`,
			`CREATE INDEX idx_completion_evidence_requirement ON completion_evidence(requirement_id,id)`,
		},
	},
	{
		Version: 11,
		Name:    "session_bridges",
		SQLite: []string{
			`CREATE TABLE session_bridges (run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,controller TEXT NOT NULL,state TEXT NOT NULL,label TEXT NOT NULL DEFAULT '',can_open INTEGER NOT NULL DEFAULT 0,can_resume INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL,expires_at TEXT NOT NULL)`,
			`CREATE INDEX idx_session_bridges_task ON session_bridges(task_id,run_id)`,
			`CREATE TABLE session_bridge_requests (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,controller TEXT NOT NULL,action TEXT NOT NULL,status TEXT NOT NULL,requested_by TEXT NOT NULL,outcome_note TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
			`CREATE INDEX idx_session_requests_controller ON session_bridge_requests(controller,status,id)`,
		},
		Postgres: []string{
			`CREATE TABLE session_bridges (run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,controller TEXT NOT NULL,state TEXT NOT NULL,label TEXT NOT NULL DEFAULT '',can_open BOOLEAN NOT NULL DEFAULT FALSE,can_resume BOOLEAN NOT NULL DEFAULT FALSE,updated_at TEXT NOT NULL,expires_at TEXT NOT NULL)`,
			`CREATE INDEX idx_session_bridges_task ON session_bridges(task_id,run_id)`,
			`CREATE TABLE session_bridge_requests (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,controller TEXT NOT NULL,action TEXT NOT NULL,status TEXT NOT NULL,requested_by TEXT NOT NULL,outcome_note TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
			`CREATE INDEX idx_session_requests_controller ON session_bridge_requests(controller,status,id)`,
		},
	},
	{Version: 12, Name: "task_dependencies", SQLite: []string{
		`CREATE TABLE task_dependencies (task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,blocked_by_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,created_by TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(task_id,blocked_by_task_id),CHECK(task_id<>blocked_by_task_id))`,
		`CREATE INDEX idx_task_dependencies_blocker ON task_dependencies(blocked_by_task_id,task_id)`,
	}, Postgres: []string{
		`CREATE TABLE task_dependencies (task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,blocked_by_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,created_by TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(task_id,blocked_by_task_id),CHECK(task_id<>blocked_by_task_id))`,
		`CREATE INDEX idx_task_dependencies_blocker ON task_dependencies(blocked_by_task_id,task_id)`,
	}},
	{Version: 13, Name: "worker_matching", SQLite: []string{
		`CREATE TABLE task_requirements (task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,requirement TEXT NOT NULL,created_by TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(task_id,requirement))`,
		`CREATE INDEX idx_task_requirements_requirement ON task_requirements(requirement,task_id)`,
		`CREATE TABLE worker_advertisements (principal TEXT PRIMARY KEY,capabilities_json TEXT NOT NULL,capacity INTEGER NOT NULL,updated_at TEXT NOT NULL,expires_at TEXT NOT NULL)`,
		`CREATE INDEX idx_worker_advertisements_expiry ON worker_advertisements(expires_at,principal)`,
	}, Postgres: []string{
		`CREATE TABLE task_requirements (task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,requirement TEXT NOT NULL,created_by TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(task_id,requirement))`,
		`CREATE INDEX idx_task_requirements_requirement ON task_requirements(requirement,task_id)`,
		`CREATE TABLE worker_advertisements (principal TEXT PRIMARY KEY,capabilities_json TEXT NOT NULL,capacity INTEGER NOT NULL,updated_at TEXT NOT NULL,expires_at TEXT NOT NULL)`,
		`CREATE INDEX idx_worker_advertisements_expiry ON worker_advertisements(expires_at,principal)`,
	}},
	{Version: 14, Name: "usage_records", SQLite: []string{
		`CREATE TABLE usage_records (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,provider TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',input_tokens INTEGER NOT NULL DEFAULT 0,output_tokens INTEGER NOT NULL DEFAULT 0,estimated_cost_micros INTEGER NOT NULL DEFAULT 0,recorded_by TEXT NOT NULL,created_at TEXT NOT NULL)`,
		`CREATE INDEX idx_usage_records_created ON usage_records(created_at,id)`,
		`CREATE INDEX idx_usage_records_task ON usage_records(task_id,id)`,
	}, Postgres: []string{
		`CREATE TABLE usage_records (id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,provider TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',input_tokens BIGINT NOT NULL DEFAULT 0,output_tokens BIGINT NOT NULL DEFAULT 0,estimated_cost_micros BIGINT NOT NULL DEFAULT 0,recorded_by TEXT NOT NULL,created_at TEXT NOT NULL)`,
		`CREATE INDEX idx_usage_records_created ON usage_records(created_at,id)`,
		`CREATE INDEX idx_usage_records_task ON usage_records(task_id,id)`,
	}},
	{Version: 15, Name: "agent_sessions", SQLite: []string{
		`DROP INDEX idx_runs_active_callsign`,
		`CREATE TABLE agent_sessions (id TEXT PRIMARY KEY,agent TEXT NOT NULL,client TEXT NOT NULL DEFAULT '',key_hash TEXT NOT NULL,callsign TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX idx_agent_sessions_agent_key ON agent_sessions(agent,key_hash)`,
		`CREATE UNIQUE INDEX idx_agent_sessions_callsign ON agent_sessions(lower(callsign))`,
		`ALTER TABLE agent_runs ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX idx_runs_session ON agent_runs(session_id)`,
	}, Postgres: []string{
		`DROP INDEX idx_runs_active_callsign`,
		`CREATE TABLE agent_sessions (id TEXT PRIMARY KEY,agent TEXT NOT NULL,client TEXT NOT NULL DEFAULT '',key_hash TEXT NOT NULL,callsign TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX idx_agent_sessions_agent_key ON agent_sessions(agent,key_hash)`,
		`CREATE UNIQUE INDEX idx_agent_sessions_callsign ON agent_sessions(lower(callsign))`,
		`ALTER TABLE agent_runs ADD COLUMN session_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX idx_runs_session ON agent_runs(session_id)`,
	}},
	{Version: 16, Name: "task_edit_provenance", SQLite: []string{
		`SELECT 1`,
	}, Postgres: []string{
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS last_edited_by TEXT NOT NULL DEFAULT ''`,
	}},
}

var messagingSchema = map[string][]string{
	"task_messages":    {"id", "task_id", "author", "author_run_id", "target_run_id", "kind", "body", "reply_to_id", "supersedes_id", "requires_ack", "created_at"},
	"message_receipts": {"message_id", "run_id", "observer", "observed_at", "acknowledged_at"},
}

var messagingIndexes = []string{"idx_task_messages_task", "idx_task_messages_target", "idx_task_messages_supersedes", "idx_message_receipts_run"}

var escalationSchema = map[string][]string{
	"task_escalations": {"id", "task_id", "run_id", "question_message_id", "answer_message_id", "blocking", "options_json", "recommendation", "selected_option", "status", "resolved_by", "created_at", "resolved_at"},
}

var escalationIndexes = []string{"idx_task_escalations_task", "idx_task_escalations_open"}

var runControlSchema = map[string][]string{
	"run_control_requests": {"id", "task_id", "target_run_id", "target_agent", "kind", "status", "requested_by", "reason", "outcome_note", "task_version", "created_at", "updated_at", "expires_at", "acknowledged_at", "decided_at", "completed_at"},
}

var runControlIndexes = []string{"idx_run_controls_task", "idx_run_controls_agent_status"}

var referenceSchema = map[string][]string{
	"task_references": {"id", "task_id", "run_id", "kind", "label", "locator", "url", "created_by", "provenance", "created_at"},
}

var referenceIndexes = []string{"idx_task_references_task", "idx_task_references_run"}

var handoffSchema = map[string][]string{
	"run_handoffs": {"id", "task_id", "run_id", "kind", "last_completed_step", "worktree", "branch", "commits_json", "pull_requests_json", "validation_json", "review_findings_json", "blocker", "next_action", "created_by", "created_at"},
}

var handoffIndexes = []string{"idx_run_handoffs_task", "idx_run_handoffs_run"}

var completionSchema = map[string][]string{
	"completion_requirements": {"id", "task_id", "kind", "label", "required", "status", "created_by", "verified_by", "waiver_reason", "created_at", "updated_at", "verified_at"},
	"completion_evidence":     {"id", "requirement_id", "task_id", "run_id", "reference_id", "note", "status", "submitted_by", "reviewed_by", "review_note", "submitted_at", "reviewed_at"},
}
var completionIndexes = []string{"idx_completion_requirements_task", "idx_completion_evidence_requirement"}

var sessionBridgeSchema = map[string][]string{
	"session_bridges":         {"run_id", "task_id", "controller", "state", "label", "can_open", "can_resume", "updated_at", "expires_at"},
	"session_bridge_requests": {"id", "task_id", "run_id", "controller", "action", "status", "requested_by", "outcome_note", "created_at", "updated_at"},
}
var sessionBridgeIndexes = []string{"idx_session_bridges_task", "idx_session_requests_controller"}
var dependencySchema = map[string][]string{"task_dependencies": {"task_id", "blocked_by_task_id", "created_by", "created_at"}}
var dependencyIndexes = []string{"idx_task_dependencies_blocker"}

var workerMatchingSchema = map[string][]string{
	"task_requirements":     {"task_id", "requirement", "created_by", "created_at"},
	"worker_advertisements": {"principal", "capabilities_json", "capacity", "updated_at", "expires_at"},
}
var workerMatchingIndexes = []string{"idx_task_requirements_requirement", "idx_worker_advertisements_expiry"}
var usageSchema = map[string][]string{"usage_records": {"id", "task_id", "run_id", "provider", "model", "input_tokens", "output_tokens", "estimated_cost_micros", "recorded_by", "created_at"}}
var usageIndexes = []string{"idx_usage_records_created", "idx_usage_records_task"}
var agentSessionSchema = map[string][]string{
	"agent_sessions": {"id", "agent", "client", "key_hash", "callsign", "created_at", "updated_at"},
}
var agentSessionIndexes = []string{"idx_agent_sessions_agent_key", "idx_agent_sessions_callsign", "idx_runs_session"}

var requiredSchema = map[string][]string{
	"tasks": {
		"id", "title", "summary", "task_type", "visibility", "created_by", "last_edited_by", "section", "project", "repository",
		"priority", "due_date", "defer_until", "recurrence", "sort_order", "reviewed_at", "status", "owner",
		"current_note", "blocker", "waiting_for", "version", "created_at", "updated_at", "completed_at",
	},
	"checklist_items":    {"id", "task_id", "label", "status", "position", "required", "note", "updated_at"},
	"agent_runs":         {"id", "task_id", "agent", "client", "status", "lease_expires_at", "last_heartbeat_at", "started_at", "ended_at"},
	"events":             {"id", "task_id", "run_id", "kind", "actor", "message", "payload", "created_at"},
	"push_subscriptions": {"endpoint", "p256dh", "auth", "owner_id", "notify_progress", "notify_reminders", "notify_summaries", "created_at", "updated_at"},
	"browser_sessions":   {"token_hash", "subject", "email", "groups_json", "created_at", "expires_at"},
	"desktop_handoffs":   {"code_hash", "subject", "email", "groups_json", "created_at", "expires_at", "confirmation_hash", "verification_code", "confirmed_at"},
	"task_templates":     {"id", "name", "title", "summary", "task_type", "section", "project", "repository", "priority", "recurrence", "checklist_json", "created_at", "updated_at"},
}

var requiredIndexes = []string{
	"idx_tasks_status_updated",
	"idx_checklist_task_position",
	"idx_runs_task",
	"idx_runs_lease",
	"idx_events_created",
	"idx_browser_sessions_expires",
	"idx_desktop_handoffs_expires",
	"idx_templates_name",
	"idx_tasks_visibility_creator",
	"idx_push_subscriptions_owner",
	"idx_desktop_handoffs_confirmation",
}

var administrativeSchema = map[string][]string{
	"agent_credentials":  {"id", "name", "principal_id", "token_hash", "created_at", "expires_at", "revoked_at", "last_used_at"},
	"revoked_principals": {"principal_id", "reason", "revoked_by", "revoked_at"},
	"admin_audit":        {"id", "actor", "action", "target", "detail", "created_at"},
	"webhook_deliveries": {"id", "event_id", "status", "attempts", "next_attempt_at", "last_error", "created_at", "updated_at"},
}

var administrativeIndexes = []string{"idx_agent_credentials_principal", "idx_admin_audit_created", "idx_webhook_deliveries_due"}

func (s *Store) migrate(ctx context.Context) error {
	if err := validateDefinedMigrations(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if s.db.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(724187452910)`); err != nil {
			return fmt.Errorf("lock PostgreSQL migrations: %w", err)
		}
	}
	if err := ensureMigrationTable(ctx, tx, s.db.dialect); err != nil {
		return err
	}

	applied, err := loadAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateMigrationSequence(applied); err != nil {
		return err
	}

	if len(applied) == 0 {
		hasSchema, err := hasApplicationSchema(ctx, tx, s.db.dialect)
		if err != nil {
			return err
		}
		if hasSchema {
			if err := adoptLegacySchema(ctx, tx, s.db.dialect); err != nil {
				return err
			}
		} else if err := executeStatements(ctx, tx, statementsFor(schemaMigrations[0], s.db.dialect)); err != nil {
			return fmt.Errorf("apply migration 1 baseline: %w", err)
		}
		if err := validateCurrentSchema(ctx, tx, s.db.dialect); err != nil {
			return fmt.Errorf("validate migration 1 baseline: %w", err)
		}
		if err := recordMigration(ctx, tx, schemaMigrations[0], s.db.dialect); err != nil {
			return err
		}
		applied = []appliedMigration{{
			Version:  schemaMigrations[0].Version,
			Name:     schemaMigrations[0].Name,
			Checksum: migrationChecksum(schemaMigrations[0], s.db.dialect),
		}}
	}

	for index, record := range applied {
		migration := schemaMigrations[index]
		expected := migrationChecksum(migration, s.db.dialect)
		if record.Checksum == "" && record.Version == 1 {
			// Releases before versioned migrations recorded only version 1. Bring
			// that known schema forward once, validate it, and seal the baseline.
			if err := adoptLegacySchema(ctx, tx, s.db.dialect); err != nil {
				return err
			}
			if err := validateCurrentSchema(ctx, tx, s.db.dialect); err != nil {
				return fmt.Errorf("validate adopted migration 1 baseline: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET name=?,checksum=? WHERE version=? AND checksum=''`, migration.Name, expected, migration.Version); err != nil {
				return fmt.Errorf("seal migration 1 baseline: %w", err)
			}
			continue
		}
		if record.Name != migration.Name || record.Checksum != expected {
			return fmt.Errorf("schema migration %d metadata does not match the immutable %q migration", record.Version, migration.Name)
		}
	}

	for index := len(applied); index < len(schemaMigrations); index++ {
		migration := schemaMigrations[index]
		if migration.Version == 16 && s.db.dialect == DialectSQLite {
			if err := ensureSQLiteColumn(ctx, tx, "tasks", "last_edited_by", `TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
			}
		}
		if err := executeStatements(ctx, tx, statementsFor(migration, s.db.dialect)); err != nil {
			return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
		}
		if err := recordMigration(ctx, tx, migration, s.db.dialect); err != nil {
			return err
		}
	}
	if err := backfillRunCallsigns(ctx, tx); err != nil {
		return err
	}
	if err := backfillAgentSessions(ctx, tx); err != nil {
		return err
	}
	if err := validateCurrentSchema(ctx, tx, s.db.dialect); err != nil {
		return err
	}
	if err := validateAdministrativeSchema(ctx, tx, s.db.dialect); err != nil {
		return err
	}
	if err := validateAdministrativeAuditProtection(ctx, tx, s.db.dialect); err != nil {
		return err
	}
	if err := validateAgentRunIdentitySchema(ctx, tx, s.db.dialect); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, agentSessionSchema, agentSessionIndexes, "agent session"); err != nil {
		return err
	}
	if err := validateMessagingSchema(ctx, tx, s.db.dialect); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, escalationSchema, escalationIndexes, "escalation"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, runControlSchema, runControlIndexes, "run control"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, referenceSchema, referenceIndexes, "reference"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, handoffSchema, handoffIndexes, "handoff"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, completionSchema, completionIndexes, "completion contract"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, sessionBridgeSchema, sessionBridgeIndexes, "session bridge"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, dependencySchema, dependencyIndexes, "dependency"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, workerMatchingSchema, workerMatchingIndexes, "worker matching"); err != nil {
		return err
	}
	if err := validateSchemaParts(ctx, tx, s.db.dialect, usageSchema, usageIndexes, "usage"); err != nil {
		return err
	}
	if s.db.dialect == DialectPostgres {
		var notificationTriggerExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_trigger trigger
			JOIN pg_class relation ON relation.oid=trigger.tgrelid
			JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
			WHERE namespace.nspname=current_schema() AND relation.relname='events'
			AND trigger.tgname='taskboard_event_notify' AND NOT trigger.tgisinternal
		)`).Scan(&notificationTriggerExists); err != nil {
			return fmt.Errorf("validate PostgreSQL event notification trigger: %w", err)
		}
		if !notificationTriggerExists {
			return fmt.Errorf("schema version %d is partial: PostgreSQL event notification trigger is missing", latestSchemaVersion)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migrations: %w", err)
	}
	if s.db.dialect == DialectSQLite {
		if _, err := s.db.ExecContext(ctx, "PRAGMA optimize"); err != nil {
			return fmt.Errorf("optimize SQLite schema: %w", err)
		}
	}
	return nil
}

func backfillRunCallsigns(ctx context.Context, tx *Tx) error {
	columns, err := tableColumns(ctx, tx, tx.dialect, "agent_runs")
	if err != nil || !columns["callsign"] {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status,ended_at,callsign FROM agent_runs ORDER BY started_at,id`)
	if err != nil {
		return fmt.Errorf("load agent runs for callsign backfill: %w", err)
	}
	type run struct {
		id, status, callsign string
		ended                sql.NullString
	}
	var runs []run
	for rows.Next() {
		var item run
		if err := rows.Scan(&item.id, &item.status, &item.ended, &item.callsign); err != nil {
			_ = rows.Close()
			return err
		}
		runs = append(runs, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	used := map[string]bool{}
	for _, item := range runs {
		if item.status == "active" && !item.ended.Valid && item.callsign != "" {
			used[strings.ToLower(item.callsign)] = true
		}
	}
	for _, item := range runs {
		if item.callsign != "" {
			continue
		}
		var selected string
		for _, candidate := range agentidentity.Candidates(item.id) {
			if item.status != "active" || item.ended.Valid || !used[strings.ToLower(candidate)] {
				selected = candidate
				break
			}
		}
		if selected == "" {
			return errors.New("no available friendly callsign for active agent run")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET callsign=? WHERE id=? AND callsign=''`, selected, item.id); err != nil {
			return fmt.Errorf("backfill callsign for run %s: %w", item.id, err)
		}
		if item.status == "active" && !item.ended.Valid {
			used[strings.ToLower(selected)] = true
		}
	}
	return nil
}

func backfillAgentSessions(ctx context.Context, tx *Tx) error {
	columns, err := tableColumns(ctx, tx, tx.dialect, "agent_runs")
	if err != nil || !columns["session_id"] {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,agent,client,callsign,started_at FROM agent_runs WHERE session_id='' ORDER BY started_at,id`)
	if err != nil {
		return fmt.Errorf("load agent runs for session backfill: %w", err)
	}
	type run struct{ id, agent, client, callsign, startedAt string }
	var runs []run
	for rows.Next() {
		var item run
		if err := rows.Scan(&item.id, &item.agent, &item.client, &item.callsign, &item.startedAt); err != nil {
			_ = rows.Close()
			return err
		}
		runs = append(runs, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range runs {
		sum := sha256.Sum256([]byte("legacy-run\x00" + item.id))
		keyHash := hex.EncodeToString(sum[:])
		candidates := append([]string{item.callsign}, agentidentity.Candidates(item.id)...)
		created := false
		for _, callsign := range candidates {
			if callsign == "" {
				continue
			}
			result, insertErr := tx.ExecContext(ctx, `INSERT INTO agent_sessions(id,agent,client,key_hash,callsign,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, item.id, item.agent, item.client, keyHash, callsign, item.startedAt, item.startedAt)
			if insertErr != nil {
				return fmt.Errorf("backfill agent session for run %s: %w", item.id, insertErr)
			}
			if count, _ := result.RowsAffected(); count == 1 {
				created = true
				break
			}
		}
		if !created {
			return fmt.Errorf("no friendly callsign is available for agent session %s", item.id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET session_id=? WHERE id=? AND session_id=''`, item.id, item.id); err != nil {
			return fmt.Errorf("link agent session for run %s: %w", item.id, err)
		}
	}
	return nil
}

func validateAgentRunIdentitySchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	columns, err := tableColumns(ctx, tx, dialect, "agent_runs")
	if err != nil {
		return err
	}
	if !columns["callsign"] || !columns["session_id"] {
		return fmt.Errorf("schema version %d is partial: required agent run identity columns are missing", latestSchemaVersion)
	}
	exists, err := indexExists(ctx, tx, dialect, "idx_runs_session")
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("schema version %d is partial: required index idx_runs_session is missing", latestSchemaVersion)
	}
	return nil
}

func validateMessagingSchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	return validateSchemaParts(ctx, tx, dialect, messagingSchema, messagingIndexes, "messaging")
}

func validateSchemaParts(ctx context.Context, tx *Tx, dialect Dialect, schema map[string][]string, indexes []string, _ string) error {
	for table, requiredColumns := range schema {
		exists, err := tableExists(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required table %s is missing", latestSchemaVersion, table)
		}
		columns, err := tableColumns(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		for _, column := range requiredColumns {
			if !columns[column] {
				return fmt.Errorf("schema version %d is partial: required column %s.%s is missing", latestSchemaVersion, table, column)
			}
		}
	}
	for _, index := range indexes {
		exists, err := indexExists(ctx, tx, dialect, index)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required index %s is missing", latestSchemaVersion, index)
		}
	}
	return nil
}

func validateAdministrativeAuditProtection(ctx context.Context, tx *Tx, dialect Dialect) error {
	for _, trigger := range []string{"admin_audit_no_update", "admin_audit_no_delete"} {
		var exists bool
		var err error
		if dialect == DialectPostgres {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM pg_trigger trigger
				JOIN pg_class relation ON relation.oid=trigger.tgrelid
				JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
				WHERE namespace.nspname=current_schema() AND relation.relname='admin_audit'
				AND trigger.tgname=? AND NOT trigger.tgisinternal
			)`, trigger).Scan(&exists)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND tbl_name='admin_audit' AND name=?)`, trigger).Scan(&exists)
		}
		if err != nil {
			return fmt.Errorf("validate administrative audit trigger %s: %w", trigger, err)
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: administrative audit trigger %s is missing", latestSchemaVersion, trigger)
		}
	}
	return nil
}

func validateDefinedMigrations() error {
	if len(schemaMigrations) != latestSchemaVersion {
		return fmt.Errorf("migration registry ends at %d but latest schema version is %d", len(schemaMigrations), latestSchemaVersion)
	}
	for index, migration := range schemaMigrations {
		if migration.Version != index+1 || strings.TrimSpace(migration.Name) == "" || len(migration.SQLite) == 0 || len(migration.Postgres) == 0 {
			return fmt.Errorf("invalid schema migration registry entry at position %d", index+1)
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, tx *Tx, dialect Dialect) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		checksum TEXT NOT NULL DEFAULT '',
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema migration table: %w", err)
	}
	if dialect == DialectPostgres {
		for _, statement := range []string{
			`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("upgrade schema migration metadata: %w", err)
			}
		}
		return nil
	}
	for _, column := range []struct{ name, definition string }{
		{"name", `TEXT NOT NULL DEFAULT ''`},
		{"checksum", `TEXT NOT NULL DEFAULT ''`},
	} {
		if err := ensureSQLiteColumn(ctx, tx, "schema_migrations", column.name, column.definition); err != nil {
			return fmt.Errorf("upgrade schema migration metadata: %w", err)
		}
	}
	return nil
}

func loadAppliedMigrations(ctx context.Context, tx *Tx) ([]appliedMigration, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("load schema migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var migrations []appliedMigration
	for rows.Next() {
		var migration appliedMigration
		if err := rows.Scan(&migration.Version, &migration.Name, &migration.Checksum); err != nil {
			return nil, err
		}
		migrations = append(migrations, migration)
	}
	return migrations, rows.Err()
}

func validateMigrationSequence(applied []appliedMigration) error {
	if len(applied) > 0 && applied[len(applied)-1].Version > latestSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", applied[len(applied)-1].Version, latestSchemaVersion)
	}
	for index, migration := range applied {
		expected := index + 1
		if migration.Version != expected {
			return fmt.Errorf("schema migration history is non-contiguous: expected version %d, found %d", expected, migration.Version)
		}
		if migration.Version > latestSchemaVersion {
			return fmt.Errorf("database schema version %d is newer than supported version %d", migration.Version, latestSchemaVersion)
		}
	}
	return nil
}

func recordMigration(ctx context.Context, tx *Tx, migration schemaMigration, dialect Dialect) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
		migration.Version, migration.Name, migrationChecksum(migration, dialect), formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("record migration %d %s: %w", migration.Version, migration.Name, err)
	}
	return nil
}

func migrationChecksum(migration schemaMigration, dialect Dialect) string {
	content := fmt.Sprintf("%d\n%s\n%s\n%s", migration.Version, migration.Name, dialect, strings.Join(statementsFor(migration, dialect), "\n-- statement --\n"))
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func statementsFor(migration schemaMigration, dialect Dialect) []string {
	if dialect == DialectPostgres {
		return migration.Postgres
	}
	return migration.SQLite
}

func executeStatements(ctx context.Context, tx *Tx, statements []string) error {
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func hasApplicationSchema(ctx context.Context, tx *Tx, dialect Dialect) (bool, error) {
	count := 0
	for table := range requiredSchema {
		exists, err := tableExists(ctx, tx, dialect, table)
		if err != nil {
			return false, err
		}
		if exists {
			count++
		}
	}
	if count != 0 && count != len(requiredSchema) {
		return false, fmt.Errorf("partial unversioned schema: found %d of %d required application tables", count, len(requiredSchema))
	}
	return count == len(requiredSchema), nil
}

func adoptLegacySchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	complete, err := hasApplicationSchema(ctx, tx, dialect)
	if err != nil {
		return err
	}
	if !complete {
		return errors.New("cannot adopt an empty schema as an existing baseline")
	}
	if dialect == DialectPostgres {
		return upgradeLegacyPostgres(ctx, tx)
	}
	return upgradeLegacySQLite(ctx, tx)
}

func upgradeLegacySQLite(ctx context.Context, tx *Tx) error {
	columns := map[string][]struct{ name, definition string }{
		"tasks": {
			{"section", `TEXT NOT NULL DEFAULT 'General'`},
			{"task_type", `TEXT NOT NULL DEFAULT 'personal'`},
			{"visibility", `TEXT NOT NULL DEFAULT 'team'`},
			{"created_by", `TEXT NOT NULL DEFAULT ''`},
			{"last_edited_by", `TEXT NOT NULL DEFAULT ''`},
			{"project", `TEXT NOT NULL DEFAULT ''`},
			{"priority", `TEXT NOT NULL DEFAULT 'normal'`},
			{"due_date", `TEXT NOT NULL DEFAULT ''`},
			{"defer_until", `TEXT NOT NULL DEFAULT ''`},
			{"recurrence", `TEXT NOT NULL DEFAULT ''`},
			{"sort_order", `INTEGER NOT NULL DEFAULT 0`},
			{"reviewed_at", `TEXT`},
		},
		"desktop_handoffs": {
			{"confirmed_at", `TEXT`},
			{"confirmation_hash", `BLOB`},
			{"verification_code", `TEXT NOT NULL DEFAULT ''`},
		},
		"task_templates": {{"task_type", `TEXT NOT NULL DEFAULT 'personal'`}},
		"push_subscriptions": {
			{"owner_id", `TEXT NOT NULL DEFAULT ''`},
			{"notify_progress", `INTEGER NOT NULL DEFAULT 1`},
			{"notify_reminders", `INTEGER NOT NULL DEFAULT 1`},
			{"notify_summaries", `INTEGER NOT NULL DEFAULT 0`},
		},
	}
	for table, additions := range columns {
		for _, column := range additions {
			if err := ensureSQLiteColumn(ctx, tx, table, column.name, column.definition); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET sort_order=rowid*1024 WHERE sort_order=0`); err != nil {
		return fmt.Errorf("backfill task ordering: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET created_by=COALESCE((SELECT actor FROM events WHERE events.task_id=tasks.id ORDER BY created_at LIMIT 1),'') WHERE created_by=''`); err != nil {
		return fmt.Errorf("backfill task creators: %w", err)
	}
	return executeStatements(ctx, tx, sqliteIndexStatements())
}

func upgradeLegacyPostgres(ctx context.Context, tx *Tx) error {
	statements := []string{
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS section TEXT NOT NULL DEFAULT 'General'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'personal'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'team'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS last_edited_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS project TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT 'normal'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS due_date TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS defer_until TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS recurrence TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sort_order BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS reviewed_at TEXT`,
		`ALTER TABLE desktop_handoffs ADD COLUMN IF NOT EXISTS confirmed_at TEXT`,
		`ALTER TABLE desktop_handoffs ADD COLUMN IF NOT EXISTS confirmation_hash BYTEA`,
		`ALTER TABLE desktop_handoffs ADD COLUMN IF NOT EXISTS verification_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_templates ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'personal'`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS notify_progress BOOLEAN NOT NULL DEFAULT TRUE`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS notify_reminders BOOLEAN NOT NULL DEFAULT TRUE`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS notify_summaries BOOLEAN NOT NULL DEFAULT FALSE`,
		`WITH ordered AS (SELECT id,row_number() OVER (ORDER BY created_at,id) AS position FROM tasks WHERE sort_order=0) UPDATE tasks SET sort_order=ordered.position*1024 FROM ordered WHERE tasks.id=ordered.id`,
		`UPDATE tasks SET created_by=COALESCE((SELECT actor FROM events WHERE events.task_id=tasks.id ORDER BY created_at LIMIT 1),'') WHERE created_by=''`,
	}
	statements = append(statements, postgresIndexStatements()...)
	if err := executeStatements(ctx, tx, statements); err != nil {
		return fmt.Errorf("upgrade legacy PostgreSQL baseline: %w", err)
	}
	return nil
}

func ensureSQLiteColumn(ctx context.Context, tx *Tx, table, column, definition string) error {
	columns, err := tableColumns(ctx, tx, DialectSQLite, table)
	if err != nil {
		return err
	}
	if columns[column] {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func validateCurrentSchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	tables := make([]string, 0, len(requiredSchema))
	for table := range requiredSchema {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		exists, err := tableExists(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required table %s is missing", latestSchemaVersion, table)
		}
		columns, err := tableColumns(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		for _, column := range requiredSchema[table] {
			if !columns[column] {
				return fmt.Errorf("schema version %d is partial: required column %s.%s is missing", latestSchemaVersion, table, column)
			}
		}
	}
	for _, index := range requiredIndexes {
		exists, err := indexExists(ctx, tx, dialect, index)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required index %s is missing", latestSchemaVersion, index)
		}
	}
	return nil
}

func validateAdministrativeSchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	for table, requiredColumns := range administrativeSchema {
		exists, err := tableExists(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required table %s is missing", latestSchemaVersion, table)
		}
		columns, err := tableColumns(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		for _, column := range requiredColumns {
			if !columns[column] {
				return fmt.Errorf("schema version %d is partial: required column %s.%s is missing", latestSchemaVersion, table, column)
			}
		}
	}
	for _, index := range administrativeIndexes {
		exists, err := indexExists(ctx, tx, dialect, index)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required index %s is missing", latestSchemaVersion, index)
		}
	}
	return nil
}

func tableExists(ctx context.Context, tx *Tx, dialect Dialect, table string) (bool, error) {
	var exists bool
	if dialect == DialectPostgres {
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=?)`, table).Scan(&exists)
		return exists, err
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
	return count == 1, err
}

func tableColumns(ctx context.Context, tx *Tx, dialect Dialect, table string) (map[string]bool, error) {
	columns := make(map[string]bool)
	if dialect == DialectPostgres {
		rows, err := tx.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=?`, table)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				return nil, err
			}
			columns[column] = true
		}
		return columns, rows.Err()
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func indexExists(ctx context.Context, tx *Tx, dialect Dialect, index string) (bool, error) {
	var count int
	if dialect == DialectPostgres {
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname=?`, index).Scan(&count)
		return count == 1, err
	}
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count)
	return count == 1, err
}

func sqliteBaselineStatements() []string {
	statements := []string{
		`CREATE TABLE tasks (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'personal',
			visibility TEXT NOT NULL DEFAULT 'team', created_by TEXT NOT NULL DEFAULT '', last_edited_by TEXT NOT NULL DEFAULT '', section TEXT NOT NULL DEFAULT 'General',
			project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal',
			due_date TEXT NOT NULL DEFAULT '', defer_until TEXT NOT NULL DEFAULT '', recurrence TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0, reviewed_at TEXT, status TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '',
			current_note TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', waiting_for TEXT NOT NULL DEFAULT '',
			version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT
		)`,
		`CREATE TABLE checklist_items (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, label TEXT NOT NULL,
			status TEXT NOT NULL, position INTEGER NOT NULL, required INTEGER NOT NULL DEFAULT 1, note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, agent TEXT NOT NULL,
			client TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, lease_expires_at TEXT NOT NULL, last_heartbeat_at TEXT NOT NULL,
			started_at TEXT NOT NULL, ended_at TEXT
		)`,
		`CREATE TABLE events (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, run_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL, actor TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
		)`,
		`CREATE TABLE push_subscriptions (
			endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL, auth TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
			notify_progress INTEGER NOT NULL DEFAULT 1, notify_reminders INTEGER NOT NULL DEFAULT 1,
			notify_summaries INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE browser_sessions (
			token_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE desktop_handoffs (
			code_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL, confirmation_hash BLOB, verification_code TEXT NOT NULL DEFAULT '', confirmed_at TEXT
		)`,
		`CREATE TABLE task_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '',
			task_type TEXT NOT NULL DEFAULT 'personal', section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '',
			repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal', recurrence TEXT NOT NULL DEFAULT '',
			checklist_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
	}
	return append(statements, sqliteIndexStatements()...)
}

func postgresBaselineStatements() []string {
	statements := []string{
		`CREATE TABLE tasks (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'personal',
			visibility TEXT NOT NULL DEFAULT 'team', created_by TEXT NOT NULL DEFAULT '', last_edited_by TEXT NOT NULL DEFAULT '', section TEXT NOT NULL DEFAULT 'General',
			project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal',
			due_date TEXT NOT NULL DEFAULT '', defer_until TEXT NOT NULL DEFAULT '', recurrence TEXT NOT NULL DEFAULT '',
			sort_order BIGINT NOT NULL DEFAULT 0, reviewed_at TEXT, status TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '',
			current_note TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', waiting_for TEXT NOT NULL DEFAULT '',
			version BIGINT NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT
		)`,
		`CREATE TABLE checklist_items (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, label TEXT NOT NULL,
			status TEXT NOT NULL, position INTEGER NOT NULL, required BOOLEAN NOT NULL DEFAULT TRUE, note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, agent TEXT NOT NULL,
			client TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, lease_expires_at TEXT NOT NULL, last_heartbeat_at TEXT NOT NULL,
			started_at TEXT NOT NULL, ended_at TEXT
		)`,
		`CREATE TABLE events (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, run_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL, actor TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
		)`,
		`CREATE TABLE push_subscriptions (
			endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL, auth TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
			notify_progress BOOLEAN NOT NULL DEFAULT TRUE, notify_reminders BOOLEAN NOT NULL DEFAULT TRUE,
			notify_summaries BOOLEAN NOT NULL DEFAULT FALSE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE browser_sessions (
			token_hash BYTEA PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE desktop_handoffs (
			code_hash BYTEA PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL, confirmation_hash BYTEA, verification_code TEXT NOT NULL DEFAULT '', confirmed_at TEXT
		)`,
		`CREATE TABLE task_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '',
			task_type TEXT NOT NULL DEFAULT 'personal', section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '',
			repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal', recurrence TEXT NOT NULL DEFAULT '',
			checklist_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
	}
	return append(statements, postgresIndexStatements()...)
}

func sqliteIndexStatements() []string { return commonIndexStatements() }

func postgresIndexStatements() []string { return commonIndexStatements() }

func commonIndexStatements() []string {
	return []string{
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_updated ON tasks(status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_checklist_task_position ON checklist_items(task_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task ON agent_runs(task_id, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_lease ON agent_runs(status, lease_expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_browser_sessions_expires ON browser_sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_desktop_handoffs_expires ON desktop_handoffs(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_templates_name ON task_templates(name)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_visibility_creator ON tasks(visibility, created_by)`,
		`CREATE INDEX IF NOT EXISTS idx_push_subscriptions_owner ON push_subscriptions(owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_desktop_handoffs_confirmation ON desktop_handoffs(confirmation_hash)`,
	}
}
