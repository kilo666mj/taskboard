package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	principal := principal(r.Context())
	if principal.Agent || principal.Role != service.RoleOwner && principal.Role != service.RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "administrator access required"})
		return false
	}
	return true
}

func listAgentCredentials(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		items, err := database.ListAgentCredentials(r.Context())
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"credentials": items})
	}
}

func createAgentCredential(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		var input struct {
			Name      string `json:"name"`
			Principal string `json:"principal_id"`
			ExpiresAt string `json:"expires_at,omitempty"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		var expires *time.Time
		if input.ExpiresAt != "" {
			value, err := time.Parse(time.RFC3339, input.ExpiresAt)
			if err != nil || !value.After(time.Now()) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at must be a future RFC3339 timestamp"})
				return
			}
			expires = &value
		}
		credential, token, err := database.CreateAgentCredential(r.Context(), input.Name, input.Principal, expires, actor(r.Context()))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"credential": credential, "token": token})
	}
}

func rotateAgentCredential(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		credential, token, err := database.RotateAgentCredential(r.Context(), r.PathValue("id"), actor(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"credential": credential, "token": token})
	}
}

func revokeAgentCredential(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		id := r.PathValue("id")
		if apiError(w, database.RevokeAgentCredential(r.Context(), id, actor(r.Context()))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func offboardPrincipal(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		principalID := strings.TrimSpace(r.PathValue("principal"))
		var input struct {
			Reason  string `json:"reason"`
			Confirm string `json:"confirm"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Confirm != principalID || principalID == actor(r.Context()) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirm must match the offboarded principal and administrators cannot offboard themselves"})
			return
		}
		if err := database.OffboardPrincipal(r.Context(), principalID, actor(r.Context()), input.Reason); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func reinstatePrincipal(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		principalID := strings.TrimSpace(r.PathValue("principal"))
		var input struct {
			Confirm string `json:"confirm"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Confirm != principalID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirm must match the reinstated principal"})
			return
		}
		if apiError(w, database.ReinstatePrincipal(r.Context(), principalID, actor(r.Context()))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adminAudit(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := database.ListAdminAudit(r.Context(), limit)
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"audit": items})
	}
}

func adminExport(database *store.Store, tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		if apiError(w, database.InsertAdminAudit(r.Context(), actor(r.Context()), "data.exported", "workspace", nil)) {
			return
		}
		taskItems, err := database.ListAllTasks(r.Context())
		if apiError(w, err) {
			return
		}
		templates, err := tasks.ListTemplates(r.Context())
		if apiError(w, err) {
			return
		}
		events, err := database.ListAllEvents(r.Context())
		if apiError(w, err) {
			return
		}
		messages, err := database.ListAllTaskMessages(r.Context())
		if apiError(w, err) {
			return
		}
		escalations, err := database.ListAllTaskEscalations(r.Context())
		if apiError(w, err) {
			return
		}
		controls, err := database.ListAllRunControls(r.Context())
		if apiError(w, err) {
			return
		}
		references, err := database.ListAllTaskReferences(r.Context())
		if apiError(w, err) {
			return
		}
		audit, err := database.ListAllAdminAudit(r.Context())
		if apiError(w, err) {
			return
		}
		handoffs, err := database.ListAllRunHandoffs(r.Context())
		if apiError(w, err) {
			return
		}
		completion, err := database.ListAllCompletionRequirements(r.Context())
		if apiError(w, err) {
			return
		}
		usage, err := database.ListAllUsageRecords(r.Context())
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"exported_at": time.Now().UTC(), "tasks": taskItems, "templates": templates, "messages": messages, "escalations": escalations, "controls": controls, "references": references, "handoffs": handoffs, "completion": completion, "usage": usage, "events": events, "admin_audit": audit})
	}
}

func deleteTaskAdministratively(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		id := r.PathValue("id")
		var input struct {
			Confirm string `json:"confirm"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Confirm != id {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirm must match the task ID"})
			return
		}
		if apiError(w, database.DeleteTask(r.Context(), id, actor(r.Context()))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func applyRetention(cfg config.Config, database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		if cfg.RetentionDays == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "retention is disabled"})
			return
		}
		var input struct {
			Confirm string `json:"confirm"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Confirm != "apply retention" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "confirm must be 'apply retention'"})
			return
		}
		before := time.Now().UTC().AddDate(0, 0, -cfg.RetentionDays)
		count, err := database.ApplyRetention(r.Context(), before, actor(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted_tasks": count, "before": before})
	}
}

func deadWebhookDeliveries(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		items, err := database.ListDeadWebhookDeliveries(r.Context(), 200)
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
	}
}

func retryWebhookDelivery(database *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(w, r) {
			return
		}
		id := r.PathValue("id")
		if apiError(w, database.RetryWebhookDelivery(r.Context(), id, actor(r.Context()))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
