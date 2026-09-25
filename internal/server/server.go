package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kilo666mj/mcpkit"
	"github.com/kilo666mj/oidcrp"
	pwakit "github.com/kilo666mj/pwa-kit"
	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/observability"
	"github.com/kilo666mj/taskboard/internal/push"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
	webassets "github.com/kilo666mj/taskboard/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var Version = "dev"

type principalKey struct{}

type taskIDInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
}
type listInput struct {
	Statuses   []model.TaskStatus   `json:"statuses,omitempty" jsonschema:"Optional task statuses to include"`
	Visibility model.TaskVisibility `json:"visibility,omitempty" jsonschema:"Optional visibility to include: agent (the pickup lane), team, or private"`
	Limit      int                  `json:"limit,omitempty" jsonschema:"Maximum tasks per page, default 100 and maximum 200"`
	Cursor     string               `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page; repeat the same filters when following it"`
}
type updateInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.UpdateRequest
}
type heartbeatInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	RunID  string `json:"run_id" jsonschema:"Agent run ULID"`
}
type claimInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.ClaimRequest
}
type messageListInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	Before string `json:"before,omitempty" jsonschema:"Optional message ULID cursor for older messages"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum messages, default 100 and maximum 200"`
}
type messageAddInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.AddMessageRequest
}
type messageReceiptInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	RunID  string `json:"run_id" jsonschema:"Agent run ULID"`
	model.MessageReceiptRequest
}
type escalationCreateInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.CreateEscalationRequest
}
type escalationListInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
}
type controlCreateInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.CreateRunControlRequest
}
type controlUpdateInput struct {
	ControlID string `json:"control_id" jsonschema:"Run control ULID"`
	model.UpdateRunControlRequest
}
type controlListInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"Maximum controls, default 100 and maximum 200"`
}
type referenceListInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
}
type referenceAddInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.AddTaskReferenceRequest
}
type handoffListInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
}
type handoffAddInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.AddRunHandoffRequest
}
type completionListInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
}
type completionEvidenceInput struct {
	TaskID        string `json:"task_id" jsonschema:"Task ULID"`
	RequirementID string `json:"requirement_id" jsonschema:"Completion requirement ULID"`
	model.SubmitCompletionEvidenceRequest
}
type sessionBridgeRegisterInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.RegisterSessionBridgeRequest
}
type sessionRequestListInput struct{}
type sessionRequestUpdateInput struct {
	RequestID string `json:"request_id" jsonschema:"Session request ULID"`
	model.UpdateSessionBridgeRequest
}
type dependencyListInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
}
type workerOutput struct {
	Worker model.WorkerAdvertisement `json:"worker"`
}
type requirementsOutput struct {
	Requirements []string `json:"requirements"`
}
type usageInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.RecordUsageRequest
}
type usageOutput struct {
	Usage model.UsageRecord `json:"usage"`
}
type moveInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.MoveRequest
}
type tasksOutput struct {
	Tasks      []model.Task `json:"tasks"`
	NextCursor string       `json:"next_cursor,omitempty" jsonschema:"Present when more tasks may follow; pass it back as cursor. A page can be short or empty while this is set."`
}
type taskOutput struct {
	Task     model.Task         `json:"task"`
	Handoffs []model.RunHandoff `json:"handoffs,omitempty"`
}
type messagesOutput struct {
	Messages []model.TaskMessage `json:"messages"`
}
type messageOutput struct {
	Message model.TaskMessage `json:"message"`
}
type escalationsOutput struct {
	Escalations []model.TaskEscalation `json:"escalations"`
}
type escalationOutput struct {
	Escalation model.TaskEscalation `json:"escalation"`
}
type controlsOutput struct {
	Controls []model.RunControlRequest `json:"controls"`
}
type controlOutput struct {
	Control model.RunControlRequest `json:"control"`
}
type reviewRequeueOutput struct {
	Task    model.Task              `json:"task"`
	Control model.RunControlRequest `json:"control"`
}
type referencesOutput struct {
	References []model.TaskReference `json:"references"`
}
type referenceOutput struct {
	Reference model.TaskReference `json:"reference"`
}
type deliveryOutput struct {
	References []model.TaskReference         `json:"references"`
	Milestones []model.DeliveryMilestone     `json:"milestones"`
	Handoffs   []model.RunHandoff            `json:"handoffs"`
	Completion []model.CompletionRequirement `json:"completion"`
}
type handoffsOutput struct {
	Handoffs []model.RunHandoff `json:"handoffs"`
}
type handoffOutput struct {
	Handoff model.RunHandoff `json:"handoff"`
}
type completionOutput struct {
	Requirements []model.CompletionRequirement `json:"requirements"`
}
type completionEvidenceOutput struct {
	Evidence model.CompletionEvidence `json:"evidence"`
}
type sessionBridgesOutput struct {
	Bridges  []model.SessionBridge        `json:"bridges"`
	Requests []model.SessionBridgeRequest `json:"requests,omitempty"`
}
type sessionBridgeOutput struct {
	Bridge model.SessionBridge `json:"bridge"`
}
type sessionRequestsOutput struct {
	Requests []model.SessionBridgeRequest `json:"requests"`
}
type sessionRequestOutput struct {
	Request model.SessionBridgeRequest `json:"request"`
}
type dependenciesOutput struct {
	Dependencies []model.TaskDependency `json:"dependencies"`
	Ready        bool                   `json:"ready"`
}
type templatesOutput struct {
	Templates []model.Template `json:"templates"`
}
type templateOutput struct {
	Template model.Template `json:"template"`
}

func New(cfg config.Config, database *store.Store, service *service.Service, notifications *push.Service, logger *slog.Logger, metricSets ...*observability.Metrics) (http.Handler, error) {
	var metrics *observability.Metrics
	if len(metricSets) > 0 {
		metrics = metricSets[0]
	}
	mcpHandler, err := mcpkit.StatelessHTTP(func(*http.Request) *mcp.Server {
		return newMCPServer(service, model.TaskType(cfg.MCPDefaultTaskType), logger)
	}, mcpkit.HTTPOptions{
		MaxRequestBodyBytes: 1 << 20, Logger: logger, DisableLocalhostProtection: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create MCP handler: %w", err)
	}

	mux := http.NewServeMux()
	sessions := newBrowserSessions(database, cfg.BrowserAuthEnabled(), logger)
	cloudflare := newCloudflareAccess(cfg)
	oidcAuth := oidcrp.New(oidcrp.Config{
		Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, RedirectURL: cfg.OIDCRedirectURL,
		AllowedSubjects: cfg.OIDCAllowedSubjects, AllowedEmails: cfg.OIDCAllowedEmails, AllowedGroups: cfg.OIDCAllowedGroups,
		StateCookieName: "taskboard_oidc", LoginPath: "/", LoginStartPath: "/api/v1/auth/oidc/start", CallbackPath: "/api/v1/auth/oidc/callback", SuccessPath: "/",
		DesktopHandoffParam: "desktop", DesktopSuccessPath: "/api/v1/auth/desktop/complete", ValidateDesktopHandoff: validDesktopHandoff,
	}, sessions)
	oidcAuth.Register(mux)
	authenticated := auth(cfg, sessions, cloudflare, logger, metrics)
	desktopExchangeLimiter := newRequestRateLimiter(10, time.Minute, 4096)
	mux.Handle("/mcp", authenticated(mcpHandler))
	mux.Handle("/mcp/", authenticated(mcpHandler))
	mux.Handle("GET /api/v1/tasks", authenticated(http.HandlerFunc(listTasks(service))))
	mux.Handle("POST /api/v1/tasks", authenticated(http.HandlerFunc(startTask(service))))
	mux.Handle("POST /api/v1/tasks/capture", authenticated(http.HandlerFunc(createTask(service))))
	mux.Handle("GET /api/v1/tasks/{id}", authenticated(http.HandlerFunc(getTask(service))))
	mux.Handle("PATCH /api/v1/tasks/{id}", authenticated(http.HandlerFunc(updateTask(service))))
	mux.Handle("GET /api/v1/tasks/{id}/messages", authenticated(http.HandlerFunc(listMessages(service))))
	mux.Handle("POST /api/v1/tasks/{id}/messages", authenticated(http.HandlerFunc(addMessage(service))))
	mux.Handle("GET /api/v1/tasks/{id}/escalations", authenticated(http.HandlerFunc(listEscalations(service))))
	mux.Handle("POST /api/v1/tasks/{id}/escalations", authenticated(http.HandlerFunc(createEscalation(service))))
	mux.Handle("POST /api/v1/tasks/{id}/escalations/{escalation}/answer", authenticated(http.HandlerFunc(resolveEscalation(service))))
	mux.Handle("GET /api/v1/tasks/{id}/controls", authenticated(http.HandlerFunc(listTaskControls(service))))
	mux.Handle("POST /api/v1/tasks/{id}/controls", authenticated(http.HandlerFunc(createTaskControl(service))))
	mux.Handle("POST /api/v1/tasks/{id}/review-requeue", authenticated(http.HandlerFunc(reviewAndRequeueTask(service))))
	mux.Handle("GET /api/v1/run-controls", authenticated(http.HandlerFunc(listPendingControls(service))))
	mux.Handle("PATCH /api/v1/run-controls/{control}", authenticated(http.HandlerFunc(updateRunControl(service))))
	mux.Handle("GET /api/v1/tasks/{id}/references", authenticated(http.HandlerFunc(listTaskReferences(service))))
	mux.Handle("POST /api/v1/tasks/{id}/references", authenticated(http.HandlerFunc(addTaskReference(service))))
	mux.Handle("GET /api/v1/tasks/{id}/delivery", authenticated(http.HandlerFunc(getTaskDelivery(service))))
	mux.Handle("GET /api/v1/tasks/{id}/handoffs", authenticated(http.HandlerFunc(listRunHandoffs(service))))
	mux.Handle("POST /api/v1/tasks/{id}/handoffs", authenticated(http.HandlerFunc(addRunHandoff(service))))
	mux.Handle("GET /api/v1/tasks/{id}/completion", authenticated(http.HandlerFunc(listCompletionRequirements(service))))
	mux.Handle("POST /api/v1/tasks/{id}/completion", authenticated(http.HandlerFunc(createCompletionRequirement(service))))
	mux.Handle("POST /api/v1/tasks/{id}/completion/{requirement}/evidence", authenticated(http.HandlerFunc(submitCompletionEvidence(service))))
	mux.Handle("PATCH /api/v1/tasks/{id}/completion/{requirement}", authenticated(http.HandlerFunc(reviewCompletionRequirement(service))))
	mux.Handle("GET /api/v1/tasks/{id}/session-bridges", authenticated(http.HandlerFunc(listTaskSessionBridges(service))))
	mux.Handle("POST /api/v1/tasks/{id}/session-requests", authenticated(http.HandlerFunc(createSessionBridgeRequest(service))))
	mux.Handle("GET /api/v1/tasks/{id}/dependencies", authenticated(http.HandlerFunc(listTaskDependencies(service))))
	mux.Handle("POST /api/v1/tasks/{id}/dependencies", authenticated(http.HandlerFunc(addTaskDependency(service))))
	mux.Handle("DELETE /api/v1/tasks/{id}/dependencies/{blockedBy}", authenticated(http.HandlerFunc(removeTaskDependency(service))))
	mux.Handle("PUT /api/v1/tasks/{id}/requirements", authenticated(http.HandlerFunc(setTaskRequirements(service))))
	mux.Handle("GET /api/v1/analytics", authenticated(http.HandlerFunc(getAnalytics(service))))
	mux.Handle("POST /api/v1/tasks/{id}/runs", authenticated(http.HandlerFunc(claimTask(service))))
	mux.Handle("PATCH /api/v1/tasks/{id}/runs/{run}", authenticated(http.HandlerFunc(renameRun(service))))
	mux.Handle("POST /api/v1/tasks/{id}/move", authenticated(http.HandlerFunc(moveTask(service))))
	mux.Handle("POST /api/v1/tasks/{id}/runs/{run}/heartbeat", authenticated(http.HandlerFunc(heartbeat(service))))
	mux.Handle("POST /api/v1/tasks/{id}/runs/{run}/message-receipts", authenticated(http.HandlerFunc(recordMessageReceipts(service))))
	mux.Handle("GET /api/v1/events", authenticated(http.HandlerFunc(events(service, database, newEventStreamLimiter(128, 4)))))
	mux.Handle("GET /api/v1/templates", authenticated(http.HandlerFunc(listTemplates(service))))
	mux.Handle("POST /api/v1/templates", authenticated(http.HandlerFunc(saveTemplate(service))))
	mux.Handle("DELETE /api/v1/templates/{id}", authenticated(http.HandlerFunc(deleteTemplate(service))))
	mux.Handle("GET /api/v1/admin/credentials", authenticated(http.HandlerFunc(listAgentCredentials(database))))
	mux.Handle("POST /api/v1/admin/credentials", authenticated(http.HandlerFunc(createAgentCredential(database))))
	mux.Handle("POST /api/v1/admin/credentials/{id}/rotate", authenticated(http.HandlerFunc(rotateAgentCredential(database))))
	mux.Handle("DELETE /api/v1/admin/credentials/{id}", authenticated(http.HandlerFunc(revokeAgentCredential(database))))
	mux.Handle("POST /api/v1/admin/principals/{principal}/offboard", authenticated(http.HandlerFunc(offboardPrincipal(database))))
	mux.Handle("DELETE /api/v1/admin/principals/{principal}/offboard", authenticated(http.HandlerFunc(reinstatePrincipal(database))))
	mux.Handle("GET /api/v1/admin/audit", authenticated(http.HandlerFunc(adminAudit(database))))
	mux.Handle("GET /api/v1/admin/export", authenticated(http.HandlerFunc(adminExport(database, service))))
	mux.Handle("DELETE /api/v1/admin/tasks/{id}", authenticated(http.HandlerFunc(deleteTaskAdministratively(database))))
	mux.Handle("POST /api/v1/admin/retention", authenticated(http.HandlerFunc(applyRetention(cfg, database))))
	mux.Handle("GET /api/v1/admin/webhooks/dead-letters", authenticated(http.HandlerFunc(deadWebhookDeliveries(database))))
	mux.Handle("POST /api/v1/admin/webhooks/{id}/retry", authenticated(http.HandlerFunc(retryWebhookDelivery(database))))
	mux.Handle("/pwa-kit/", pwakit.Handler())
	mux.Handle("GET /api/v1/push/key", authenticated(http.HandlerFunc(pushKey(notifications))))
	mux.Handle("POST /api/v1/push/subscriptions", authenticated(http.HandlerFunc(savePushSubscription(notifications))))
	mux.Handle("PATCH /api/v1/push/subscriptions", authenticated(http.HandlerFunc(updatePushPreferences(notifications))))
	mux.Handle("DELETE /api/v1/push/subscriptions", authenticated(http.HandlerFunc(deletePushSubscription(notifications))))
	mux.HandleFunc("GET /api/v1/session", sessionState(cfg, sessions, cloudflare))
	mux.HandleFunc("DELETE /api/v1/session", logout(cfg, sessions))
	mux.Handle("POST /api/v1/auth/desktop/session", rateLimited(desktopExchangeLimiter, desktopSessionExchange(sessions)))
	mux.HandleFunc("GET /api/v1/auth/desktop/complete", desktopLoginComplete(sessions))
	mux.HandleFunc("POST /api/v1/auth/desktop/confirm", desktopConfirm(sessions))
	mux.HandleFunc("POST /api/v1/auth/desktop/cancel", desktopCancel(sessions))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "taskboard", "version": Version})
	})
	mux.Handle("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		err := service.Ready(ctx)
		if metrics != nil {
			metrics.ObserveDatabasePing(err)
		}
		if err != nil {
			logger.Error("readiness check failed")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}))
	assets, err := fs.Sub(webassets.Files, ".")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(assets))
	mux.Handle("/", spaHandler(fileServer, assets))
	handler := securityHeaders(logging(logger, requireAllowedHost(cfg.AllowedHosts, mux)))
	if metrics != nil {
		handler = metrics.HTTP(handler)
	}
	return handler, nil
}

func pushKey(notifications *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !principal(r.Context()).Can(service.PermissionPushManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		if !notifications.Enabled() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Web Push is not configured"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"public_key": notifications.PublicKey()})
	}
}

func savePushSubscription(notifications *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !principal(r.Context()).Can(service.PermissionPushManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		if !notifications.Enabled() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Web Push is not configured"})
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		input, err := pwakit.DecodeSubscription(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		if apiError(w, notifications.Save(r.Context(), store.PushSubscription{Endpoint: input.Endpoint, P256DH: input.Keys.P256dh, Auth: input.Keys.Auth, OwnerID: actor(r.Context())})) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deletePushSubscription(notifications *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !principal(r.Context()).Can(service.PermissionPushManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		var input struct {
			Endpoint string `json:"endpoint"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if input.Endpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required"})
			return
		}
		if err := notifications.Delete(r.Context(), input.Endpoint, actor(r.Context())); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not remove subscription"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func updatePushPreferences(notifications *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !principal(r.Context()).Can(service.PermissionPushManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		var input struct {
			Endpoint  string `json:"endpoint"`
			Progress  bool   `json:"progress"`
			Reminders bool   `json:"reminders"`
			Summaries bool   `json:"summaries"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if strings.TrimSpace(input.Endpoint) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required"})
			return
		}
		if apiError(w, notifications.UpdatePreferences(r.Context(), input.Endpoint, actor(r.Context()), input.Progress, input.Reminders, input.Summaries)) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func newMCPServer(tasks *service.Service, defaultTaskType model.TaskType, logger *slog.Logger) *mcp.Server {
	if !model.IsTaskType(defaultTaskType) {
		defaultTaskType = model.TaskWork
	}
	server := mcpkit.MustServer(mcpkit.ServerConfig{
		Name: "taskboard", Version: Version, Logger: logger,
		Instructions: "Use task_create to capture future work without beginning execution. Before substantive agent work, start or claim the task and keep its task and run IDs. Controllers should supply the same opaque agent_session_key on every start or claim made by one agent session so its friendly callsign remains stable across tasks; never use a thread ID or secret as that key. As soon as one checklist item is finished, call task_update with that one complete_item_id and the next current_item_id; do not save completed checklist updates until the end. Heartbeat during long work, act on stale-progress hints, process pending_messages, acknowledge them with task_message_ack, and process pending_controls through acknowledged, accepted or rejected, and completed states. Also poll task_control_list because ended runs cannot heartbeat. Add typed delivery references as branches, commits, PRs, CI runs, reviews, and deployments appear. Messages and references never change workflow or control state by themselves. Record blockers immediately, and complete only after all required checklist items are done or skipped with a reason. Unattended clients should send a stable idempotency_key for each mutating task operation and reuse it only when retrying the identical request.",
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_start", Description: "Register substantial work before beginning. Creates a durable task, checklist, and leased task run; controllers should reuse one opaque agent_session_key across tasks handled by the same agent session.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input model.StartRequest) (*mcp.CallToolResult, model.StartResult, error) {
		if input.Type == "" {
			input.Type = defaultTaskType
		}
		input.Client = mcpClient(request)
		result, err := tasks.StartFor(ctx, input, mcpPrincipal(ctx))
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_create", Description: "Capture future work without starting execution. Creates a queued task with no agent run; title is the only required field. When the caller is a person delegating through Cloudflare Access, the task is created as that person (private unless visibility is set); pass visibility \"agent\" only when asked for an agent-lane task.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input model.CreateRequest) (*mcp.CallToolResult, taskOutput, error) {
		if input.Type == "" {
			input.Type = defaultTaskType
		}
		task, err := tasks.CreateFor(ctx, input, mcpPrincipal(ctx))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_update", Description: "Atomically update assignment, section, checklist progress, and user-visible status. Immediately after finishing each bounded step, send that one complete_item_id and the next current_item_id instead of batching progress at the end. expected_version prevents overwriting another agent's changes.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input updateInput) (*mcp.CallToolResult, taskOutput, error) {
		task, err := tasks.UpdateFor(ctx, input.TaskID, input.UpdateRequest, mcpPrincipal(ctx))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_claim", Description: "Claim an existing non-terminal task and receive a fresh leased run. Reuse the controller-provided agent_session_key to retain this agent session's callsign across tasks.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input claimInput) (*mcp.CallToolResult, model.StartResult, error) {
		input.Client = mcpClient(request)
		result, err := tasks.ClaimFor(ctx, input.TaskID, input.ClaimRequest, mcpPrincipal(ctx))
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_move", Description: "Move an open task up or down within its section while preserving optimistic version checks.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input moveInput) (*mcp.CallToolResult, taskOutput, error) {
		task, err := tasks.MoveFor(ctx, input.TaskID, input.MoveRequest, mcpPrincipal(ctx))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_complete", Description: "Mark a task done. Rejected while any required checklist item remains open; skipped items require a recorded reason.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input updateInput) (*mcp.CallToolResult, taskOutput, error) {
		input.Status = model.TaskDone
		task, err := tasks.UpdateFor(ctx, input.TaskID, input.UpdateRequest, mcpPrincipal(ctx))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_heartbeat", Description: "Renew an active run lease during long work and report checklist progress age. A stale hint is advisory: respond by recording real completed items with task_update; heartbeat never infers or changes checklist completion.", Annotations: mcpkit.Mutating(true, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input heartbeatInput) (*mcp.CallToolResult, model.HeartbeatResult, error) {
		result, err := tasks.HeartbeatFor(ctx, input.TaskID, input.RunID, mcpPrincipal(ctx))
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_message_list", Description: "List the append-only conversation for an owned task. Workflow transitions remain in task events rather than duplicate status messages.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input messageListInput) (*mcp.CallToolResult, messagesOutput, error) {
		messages, err := tasks.ListMessagesFor(ctx, input.TaskID, input.Before, input.Limit, mcpPrincipal(ctx))
		return nil, messagesOutput{Messages: messages}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_message_add", Description: "Append a note, question, or answer from the active agent run. Messages never change task status, checklist items, or control state. Agents cannot issue instructions or require acknowledgement.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input messageAddInput) (*mcp.CallToolResult, messageOutput, error) {
		message, err := tasks.AddMessageFor(ctx, input.TaskID, input.AddMessageRequest, mcpPrincipal(ctx))
		return nil, messageOutput{Message: message}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_message_ack", Description: "Record messages explicitly observed or acknowledged by this active run. Instructions requiring acknowledgement remain in heartbeat responses until acknowledged; fetching alone never changes receipt state.", Annotations: mcpkit.Mutating(true, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input messageReceiptInput) (*mcp.CallToolResult, messagesOutput, error) {
		pending, err := tasks.RecordMessageReceiptsFor(ctx, input.TaskID, input.RunID, input.MessageReceiptRequest, mcpPrincipal(ctx))
		return nil, messagesOutput{Messages: pending}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_escalation_list", Description: "List structured questions, choices, recommendations, and recorded answers for an owned task.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input escalationListInput) (*mcp.CallToolResult, escalationsOutput, error) {
		items, err := tasks.ListEscalationsFor(ctx, input.TaskID, mcpPrincipal(ctx))
		return nil, escalationsOutput{Escalations: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_escalate", Description: "Ask a structured question from an active run. A blocking escalation atomically ends the run and waits the task; after a human answers, claim the queued task to create a replacement run.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input escalationCreateInput) (*mcp.CallToolResult, escalationOutput, error) {
		item, err := tasks.CreateEscalationFor(ctx, input.TaskID, input.CreateEscalationRequest, mcpPrincipal(ctx))
		return nil, escalationOutput{Escalation: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_control_list", Description: "Poll actionable pause, cancel, resume, and retry requests addressed to this controller principal. This works even when the target run has ended.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input controlListInput) (*mcp.CallToolResult, controlsOutput, error) {
		items, err := tasks.ListPendingRunControlsFor(ctx, input.Limit, mcpPrincipal(ctx))
		return nil, controlsOutput{Controls: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_control_update", Description: "Acknowledge, accept, reject, complete, or expire a control addressed to this controller. Only completed controls mutate task/run state; completion requires the latest task version.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input controlUpdateInput) (*mcp.CallToolResult, controlOutput, error) {
		item, err := tasks.UpdateRunControlFor(ctx, input.ControlID, input.UpdateRunControlRequest, mcpPrincipal(ctx))
		return nil, controlOutput{Control: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_reference_list", Description: "List immutable typed delivery references and their authenticated provenance for an owned task.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input referenceListInput) (*mcp.CallToolResult, referencesOutput, error) {
		items, err := tasks.ListTaskReferencesFor(ctx, input.TaskID, mcpPrincipal(ctx))
		return nil, referencesOutput{References: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_reference_add", Description: "Attach a typed Linear, repository, worktree, branch, commit, pull request, CI, deployment, screenshot, or review reference from the active run. URLs must be absolute HTTPS links without embedded credentials.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input referenceAddInput) (*mcp.CallToolResult, referenceOutput, error) {
		item, err := tasks.AddTaskReferenceFor(ctx, input.TaskID, input.AddTaskReferenceRequest, mcpPrincipal(ctx))
		return nil, referenceOutput{Reference: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_delivery_get", Description: "Get typed references together with the ordered delivery milestones derived from those references and selected Taskboard lifecycle events.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input referenceListInput) (*mcp.CallToolResult, deliveryOutput, error) {
		references, err := tasks.ListTaskReferencesFor(ctx, input.TaskID, mcpPrincipal(ctx))
		if err != nil {
			return nil, deliveryOutput{}, err
		}
		milestones, err := tasks.DeliveryMilestonesFor(ctx, input.TaskID, mcpPrincipal(ctx))
		if err != nil {
			return nil, deliveryOutput{}, err
		}
		handoffs, err := tasks.ListRunHandoffsFor(ctx, input.TaskID, mcpPrincipal(ctx))
		if err != nil {
			return nil, deliveryOutput{}, err
		}
		completion, err := tasks.ListCompletionRequirementsFor(ctx, input.TaskID, mcpPrincipal(ctx))
		return nil, deliveryOutput{References: references, Milestones: milestones, Handoffs: handoffs, Completion: completion}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_handoff_list", Description: "List append-only continuation snapshots from prior and current runs. Replacement runs should inspect these immediately after claiming work.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input handoffListInput) (*mcp.CallToolResult, handoffsOutput, error) {
		items, err := tasks.ListRunHandoffsFor(ctx, input.TaskID, mcpPrincipal(ctx))
		return nil, handoffsOutput{Handoffs: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_handoff_add", Description: "Append a concise structured checkpoint or final handoff for the active run. Do not include prompts, reasoning, credentials, or raw tool output.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input handoffAddInput) (*mcp.CallToolResult, handoffOutput, error) {
		item, err := tasks.AddRunHandoffFor(ctx, input.TaskID, input.AddRunHandoffRequest, mcpPrincipal(ctx))
		return nil, handoffOutput{Handoff: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_completion_get", Description: "List the human-owned completion contract and submitted evidence for an owned task.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input completionListInput) (*mcp.CallToolResult, completionOutput, error) {
		items, err := tasks.ListCompletionRequirementsFor(ctx, input.TaskID, mcpPrincipal(ctx))
		return nil, completionOutput{Requirements: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_completion_evidence_submit", Description: "Submit concise evidence for one completion requirement from the active run. Evidence remains pending until a human verifies it.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input completionEvidenceInput) (*mcp.CallToolResult, completionEvidenceOutput, error) {
		item, err := tasks.SubmitCompletionEvidenceFor(ctx, input.TaskID, input.RequirementID, input.SubmitCompletionEvidenceRequest, mcpPrincipal(ctx))
		return nil, completionEvidenceOutput{Evidence: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_session_register", Description: "Advertise trusted open/resume availability for an active run. The controller retains the private run-to-session mapping; Taskboard stores no URL or session secret.", Annotations: mcpkit.Mutating(true, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input sessionBridgeRegisterInput) (*mcp.CallToolResult, sessionBridgeOutput, error) {
		item, err := tasks.RegisterSessionBridgeFor(ctx, input.TaskID, input.RegisterSessionBridgeRequest, mcpPrincipal(ctx))
		return nil, sessionBridgeOutput{Bridge: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_session_request_list", Description: "Poll open/resume requests addressed to this controller principal.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input sessionRequestListInput) (*mcp.CallToolResult, sessionRequestsOutput, error) {
		items, err := tasks.ListSessionBridgeRequestsFor(ctx, mcpPrincipal(ctx))
		return nil, sessionRequestsOutput{Requests: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_session_request_update", Description: "Acknowledge, complete, or reject a trusted session action request addressed to this controller.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input sessionRequestUpdateInput) (*mcp.CallToolResult, sessionRequestOutput, error) {
		item, err := tasks.UpdateSessionBridgeRequestFor(ctx, input.RequestID, input.UpdateSessionBridgeRequest, mcpPrincipal(ctx))
		return nil, sessionRequestOutput{Request: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_dependency_list", Description: "List blocked_by edges and derived readiness for an owned task. Readiness is separate from execution status.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input dependencyListInput) (*mcp.CallToolResult, dependenciesOutput, error) {
		items, err := tasks.ListTaskDependenciesFor(ctx, input.TaskID, mcpPrincipal(ctx))
		ready := true
		for _, item := range items {
			if !item.Satisfied {
				ready = false
			}
		}
		return nil, dependenciesOutput{Dependencies: items, Ready: ready}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "worker_advertise", Description: "Advertise this controller principal's short-lived operational capabilities and capacity for task matching. This does not grant security authorization.", Annotations: mcpkit.Mutating(true, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input model.AdvertiseWorkerRequest) (*mcp.CallToolResult, workerOutput, error) {
		item, err := tasks.AdvertiseWorkerFor(ctx, input, mcpPrincipal(ctx))
		return nil, workerOutput{Worker: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_usage_record", Description: "Record numeric token and estimated-cost totals for the active run. Never include prompts, reasoning, credentials, or tool output.", Annotations: mcpkit.Mutating(true, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input usageInput) (*mcp.CallToolResult, usageOutput, error) {
		item, err := tasks.RecordUsageFor(ctx, input.TaskID, input.RecordUsageRequest, mcpPrincipal(ctx))
		return nil, usageOutput{Usage: item}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_get", Description: "Get one task with its ordered checklist and agent runs.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input taskIDInput) (*mcp.CallToolResult, taskOutput, error) {
		task, err := tasks.GetFor(ctx, input.TaskID, mcpPrincipal(ctx))
		if err != nil {
			return nil, taskOutput{}, err
		}
		handoffs, err := tasks.ListRunHandoffsFor(ctx, input.TaskID, mcpPrincipal(ctx))
		return nil, taskOutput{Task: task, Handoffs: handoffs}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_list", Description: "List agent-pickup work and team work explicitly assigned to this agent, optionally filtered by status and visibility. Queued and stale tasks appear only when ready and matching this worker's advertised capabilities. Follow next_cursor until it is absent to see every task.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, tasksOutput, error) {
		page, err := tasks.ListFor(ctx, model.ListTasksRequest{Statuses: input.Statuses, Visibility: input.Visibility, Limit: input.Limit, Cursor: input.Cursor}, mcpPrincipal(ctx))
		return nil, tasksOutput{Tasks: page.Tasks, NextCursor: page.NextCursor}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_template_list", Description: "List reusable Taskboard task and checklist templates.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, templatesOutput, error) {
		items, err := tasks.ListTemplatesFor(ctx, mcpPrincipal(ctx))
		return nil, templatesOutput{Templates: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_template_save", Description: "Create or replace a reusable task and checklist template by name.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, input model.TemplateRequest) (*mcp.CallToolResult, templateOutput, error) {
		item, err := tasks.SaveTemplateFor(ctx, input, mcpPrincipal(ctx))
		return nil, templateOutput{Template: item}, err
	})
	return server
}

func listTemplates(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListTemplatesFor(r.Context(), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, templatesOutput{Templates: items})
	}
}
func saveTemplate(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.TemplateRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.SaveTemplateFor(r.Context(), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, templateOutput{Template: item})
	}
}
func deleteTemplate(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiError(w, tasks.DeleteTemplateFor(r.Context(), r.PathValue("id"), principal(r.Context()))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func listTasks(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var statuses []model.TaskStatus
		for _, value := range r.URL.Query()["status"] {
			for _, item := range strings.Split(value, ",") {
				if item = strings.TrimSpace(item); item != "" {
					statuses = append(statuses, model.TaskStatus(item))
				}
			}
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, err := tasks.ListFor(r.Context(), model.ListTasksRequest{Statuses: statuses, Visibility: model.TaskVisibility(r.URL.Query().Get("visibility")), Limit: limit, Cursor: r.URL.Query().Get("cursor")}, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, tasksOutput{Tasks: page.Tasks, NextCursor: page.NextCursor})
	}
}
func startTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.StartRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		result, err := tasks.StartFor(r.Context(), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}
func createTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.CreateRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		task, err := tasks.CreateFor(r.Context(), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, taskOutput{Task: task})
	}
}
func getTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		task, err := tasks.GetFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		handoffs, _ := tasks.ListRunHandoffsFor(r.Context(), task.ID, principal(r.Context()))
		writeJSON(w, http.StatusOK, taskOutput{Task: task, Handoffs: handoffs})
	}
}
func updateTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.UpdateRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		task, err := tasks.UpdateFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, taskOutput{Task: task})
	}
}
func listMessages(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		messages, err := tasks.ListMessagesFor(r.Context(), r.PathValue("id"), r.URL.Query().Get("before"), limit, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, messagesOutput{Messages: messages})
	}
}
func addMessage(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.AddMessageRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		message, err := tasks.AddMessageFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, messageOutput{Message: message})
	}
}
func listEscalations(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListEscalationsFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, escalationsOutput{Escalations: items})
	}
}
func createEscalation(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.CreateEscalationRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.CreateEscalationFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, escalationOutput{Escalation: item})
	}
}
func resolveEscalation(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.ResolveEscalationRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.ResolveEscalationFor(r.Context(), r.PathValue("id"), r.PathValue("escalation"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, escalationOutput{Escalation: item})
	}
}
func listTaskControls(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListTaskRunControlsFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, controlsOutput{Controls: items})
	}
}
func createTaskControl(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.CreateRunControlRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.CreateRunControlFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, controlOutput{Control: item})
	}
}
func reviewAndRequeueTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.ReviewRequeueRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		result, err := tasks.ReviewAndRequeueFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, reviewRequeueOutput{Task: result.Task, Control: result.Control})
	}
}
func listPendingControls(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := tasks.ListPendingRunControlsFor(r.Context(), limit, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, controlsOutput{Controls: items})
	}
}
func updateRunControl(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.UpdateRunControlRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.UpdateRunControlFor(r.Context(), r.PathValue("control"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, controlOutput{Control: item})
	}
}
func listTaskReferences(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListTaskReferencesFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, referencesOutput{References: items})
	}
}
func addTaskReference(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.AddTaskReferenceRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.AddTaskReferenceFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, referenceOutput{Reference: item})
	}
}
func getTaskDelivery(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		references, err := tasks.ListTaskReferencesFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		milestones, err := tasks.DeliveryMilestonesFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		handoffs, _ := tasks.ListRunHandoffsFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		completion, _ := tasks.ListCompletionRequirementsFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		writeJSON(w, http.StatusOK, deliveryOutput{References: references, Milestones: milestones, Handoffs: handoffs, Completion: completion})
	}
}
func listRunHandoffs(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListRunHandoffsFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, handoffsOutput{Handoffs: items})
	}
}
func addRunHandoff(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.AddRunHandoffRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.AddRunHandoffFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, handoffOutput{Handoff: item})
	}
}
func listCompletionRequirements(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListCompletionRequirementsFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, completionOutput{Requirements: items})
	}
}
func createCompletionRequirement(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.CreateCompletionRequirementRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.CreateCompletionRequirementFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"requirement": item})
	}
}
func submitCompletionEvidence(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.SubmitCompletionEvidenceRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.SubmitCompletionEvidenceFor(r.Context(), r.PathValue("id"), r.PathValue("requirement"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, completionEvidenceOutput{Evidence: item})
	}
}
func reviewCompletionRequirement(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.ReviewCompletionRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.ReviewCompletionRequirementFor(r.Context(), r.PathValue("id"), r.PathValue("requirement"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"requirement": item})
	}
}
func listTaskSessionBridges(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListTaskSessionBridgesFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, sessionBridgesOutput{Bridges: items})
	}
}
func createSessionBridgeRequest(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.CreateSessionBridgeRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		item, err := tasks.CreateSessionBridgeRequestFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusAccepted, sessionRequestOutput{Request: item})
	}
}
func listTaskDependencies(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListTaskDependenciesFor(r.Context(), r.PathValue("id"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		ready := true
		for _, item := range items {
			if !item.Satisfied {
				ready = false
			}
		}
		writeJSON(w, http.StatusOK, dependenciesOutput{Dependencies: items, Ready: ready})
	}
}
func addTaskDependency(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.AddTaskDependencyRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		task, err := tasks.AddTaskDependencyFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, taskOutput{Task: task})
	}
}
func removeTaskDependency(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		version, _ := strconv.ParseInt(r.URL.Query().Get("expected_version"), 10, 64)
		task, err := tasks.RemoveTaskDependencyFor(r.Context(), r.PathValue("id"), r.PathValue("blockedBy"), version, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, taskOutput{Task: task})
	}
}
func setTaskRequirements(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.SetTaskRequirementsRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		task, err := tasks.SetTaskRequirementsFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, taskOutput{Task: task})
	}
}
func getAnalytics(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		summary, err := tasks.AnalyticsFor(r.Context(), days, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"analytics": summary})
	}
}
func recordMessageReceipts(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.MessageReceiptRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		pending, err := tasks.RecordMessageReceiptsFor(r.Context(), r.PathValue("id"), r.PathValue("run"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, messagesOutput{Messages: pending})
	}
}
func claimTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.ClaimRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		result, err := tasks.ClaimFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, result)
	}
}
func renameRun(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.RenameRunRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		task, err := tasks.RenameRunFor(r.Context(), r.PathValue("id"), r.PathValue("run"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, taskOutput{Task: task})
	}
}
func moveTask(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input model.MoveRequest
		if !decodeJSON(w, r, &input) {
			return
		}
		task, err := tasks.MoveFor(r.Context(), r.PathValue("id"), input, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, taskOutput{Task: task})
	}
}
func heartbeat(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := tasks.HeartbeatFor(r.Context(), r.PathValue("id"), r.PathValue("run"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

const eventKeepaliveInterval = 20 * time.Second

type eventStreamLimiter struct {
	mu              sync.Mutex
	global          int
	byPrincipal     map[string]int
	maxGlobal       int
	maxPerPrincipal int
}

type rateWindow struct {
	count int
	reset time.Time
}

type requestRateLimiter struct {
	mu         sync.Mutex
	entries    map[string]rateWindow
	overflow   rateWindow
	limit      int
	window     time.Duration
	maxEntries int
}

func newRequestRateLimiter(limit int, window time.Duration, maxEntries int) *requestRateLimiter {
	return &requestRateLimiter{entries: make(map[string]rateWindow), limit: limit, window: window, maxEntries: maxEntries}
}

func (l *requestRateLimiter) allow(key string, now time.Time) bool {
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, exists := l.entries[key]
	overflow := false
	if !exists && len(l.entries) >= l.maxEntries {
		for candidate, current := range l.entries {
			if !now.Before(current.reset) {
				delete(l.entries, candidate)
			}
		}
		if len(l.entries) >= l.maxEntries {
			entry = l.overflow
			exists = !entry.reset.IsZero()
			overflow = true
		}
	}
	if !exists || !now.Before(entry.reset) {
		entry = rateWindow{reset: now.Add(l.window)}
	}
	if entry.count >= l.limit {
		if overflow {
			l.overflow = entry
		} else {
			l.entries[key] = entry
		}
		return false
	}
	entry.count++
	if overflow {
		l.overflow = entry
	} else {
		l.entries[key] = entry
	}
	return true
}

func requestRateKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func rateLimited(limiter *requestRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(requestRateKey(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newEventStreamLimiter(maxGlobal, maxPerPrincipal int) *eventStreamLimiter {
	return &eventStreamLimiter{byPrincipal: make(map[string]int), maxGlobal: maxGlobal, maxPerPrincipal: maxPerPrincipal}
}

func (l *eventStreamLimiter) acquire(principalID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.global >= l.maxGlobal || l.byPrincipal[principalID] >= l.maxPerPrincipal {
		return false
	}
	l.global++
	l.byPrincipal[principalID]++
	return true
}

func (l *eventStreamLimiter) release(principalID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global--
	l.byPrincipal[principalID]--
	if l.byPrincipal[principalID] == 0 {
		delete(l.byPrincipal, principalID)
	}
}

type principalRevocationChecker interface {
	PrincipalRevoked(context.Context, string) (bool, error)
}

func events(tasks *service.Service, revocations principalRevocationChecker, limiter *eventStreamLimiter) http.HandlerFunc {
	return eventsWithKeepalive(tasks, revocations, eventKeepaliveInterval, limiter)
}

func eventsWithKeepalive(tasks *service.Service, revocations principalRevocationChecker, keepaliveInterval time.Duration, limiter *eventStreamLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authenticatedPrincipal := principal(r.Context())
		if !authenticatedPrincipal.Can(service.PermissionEventStream) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		principalID := authenticatedPrincipal.ID
		if revoked, err := revocations.PrincipalRevoked(r.Context(), principalID); err != nil || revoked {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		if !limiter.acquire(principalID) {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many live event streams"})
			return
		}
		defer limiter.release(principalID)
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unavailable"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		channel, cancel := tasks.Subscribe()
		defer cancel()
		if _, err := fmt.Fprint(w, "retry: 2000\nevent: ready\ndata: {}\n\n"); err != nil {
			return
		}
		flusher.Flush()
		keepalive := time.NewTicker(keepaliveInterval)
		defer keepalive.Stop()
		for {
			select {
			case event, ok := <-channel:
				if !ok {
					return
				}
				if revoked, err := revocations.PrincipalRevoked(r.Context(), principalID); err != nil || revoked {
					return
				}
				if _, err := tasks.GetFor(r.Context(), event.TaskID, principal(r.Context())); err != nil {
					if _, changed := event.Payload["visibility"]; !changed {
						continue
					}
					event = model.Event{ID: event.ID, TaskID: event.TaskID, Kind: "task.visibility_changed", Payload: map[string]any{"reload": true}, CreatedAt: event.CreatedAt}
				}
				payload, _ := json.Marshal(event)
				if _, err := fmt.Fprintf(w, "id: %s\nevent: task\ndata: %s\n\n", event.ID, payload); err != nil {
					return
				}
				flusher.Flush()
			case <-keepalive.C:
				if revoked, err := revocations.PrincipalRevoked(r.Context(), principalID); err != nil || revoked {
					return
				}
				if _, err := fmt.Fprint(w, "event: ping\ndata: {}\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

func auth(cfg config.Config, sessions *browserSessions, cloudflare *cloudflareAccess, logger *slog.Logger, metricSets ...*observability.Metrics) func(http.Handler) http.Handler {
	var metrics *observability.Metrics
	if len(metricSets) > 0 {
		metrics = metricSets[0]
	}
	authFailures := newRequestRateLimiter(30, time.Minute, 4096)
	mutations := newRequestRateLimiter(300, time.Minute, 4096)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mcpRequest := r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/")
			if !mcpRequest && !safeMethod(r.Method) && !safeBrowserMutation(r) {
				if metrics != nil {
					metrics.ObserveAuth("browser_mutation", "rejected")
				}
				logger.Warn("rejected unsafe browser mutation")
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
				return
			}
			if cfg.AllowInsecure && cfg.AuthToken == "" {
				if metrics != nil {
					metrics.ObserveAuth("local", "success")
				}
				authenticatedPrincipal := service.HumanPrincipalWithRole("local", roleForGroups(cfg, nil))
				if mcpRequest {
					authenticatedPrincipal = agentPrincipal(cfg, "agent:local")
				}
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, authenticatedPrincipal)))
				return
			}
			valid, mechanism := false, "session"
			authenticatedPrincipal := service.Principal{}
			if mcpRequest {
				mechanism = "token"
				values := r.Header.Values("Authorization")
				accessValues := r.Header.Values(cloudflareAccessJWTHeader)
				if len(values) > 0 && len(accessValues) > 0 {
					if metrics != nil {
						metrics.ObserveAuth("ambiguous", "rejected")
					}
					logger.Warn("rejected request with ambiguous credentials")
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
					return
				}
				if cloudflare != nil && len(accessValues) > 0 {
					mechanism = "cloudflare_access"
					if identity, err := cloudflare.identity(r); err == nil {
						valid = true
						authenticatedPrincipal = agentPrincipal(cfg, identity.Subject)
						if cfg.MCPHumanDelegation && !identity.Service {
							person := service.HumanPrincipalWithRole(identity.Subject, roleForGroups(cfg, identity.Groups))
							authenticatedPrincipal.OnBehalfOf = &person
						}
					}
				} else if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") {
					presented := strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer "))
					if secureEqual(presented, cfg.AuthToken) {
						valid = true
						authenticatedPrincipal = agentPrincipal(cfg, "agent:shared")
					} else if principalID, credentialValid, err := sessions.store.AuthenticateAgentCredential(r.Context(), presented); err == nil && credentialValid {
						valid = true
						authenticatedPrincipal = agentPrincipal(cfg, principalID)
					}
				}
			} else if cloudflare != nil {
				mechanism = "cloudflare_access"
				if identity, err := cloudflare.identity(r); err == nil && !identity.Service {
					valid = true
					authenticatedPrincipal = service.HumanPrincipalWithRole(identity.Subject, roleForGroups(cfg, identity.Groups))
				}
			} else if identity, ok := sessions.identity(r.Context(), r); ok {
				valid = true
				authenticatedPrincipal = browserPrincipal(cfg, identity)
			}
			if !valid {
				if !authFailures.allow(requestRateKey(r), time.Now()) {
					if metrics != nil {
						metrics.ObserveAuth(mechanism, "rate_limited")
					}
					w.Header().Set("Retry-After", "60")
					writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many authentication failures"})
					return
				}
				if metrics != nil {
					metrics.ObserveAuth(mechanism, "failure")
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="taskboard"`)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
			if revoked, err := sessions.store.PrincipalRevoked(r.Context(), authenticatedPrincipal.ID); err != nil || revoked {
				if metrics != nil {
					metrics.ObserveAuth(mechanism, "revoked")
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
			if metrics != nil {
				metrics.ObserveAuth(mechanism, "success")
			}
			if !safeMethod(r.Method) && !mutations.allow(authenticatedPrincipal.ID, time.Now()) {
				w.Header().Set("Retry-After", "60")
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many mutations"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, authenticatedPrincipal)))
		})
	}
}

func sessionState(cfg config.Config, sessions *browserSessions, cloudflare *cloudflareAccess) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.AllowInsecure && cfg.AuthToken == "" {
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "auth_mode": "local", "oidc_enabled": cfg.OIDCEnabled(), "cloudflare_access_enabled": false, "identity": "local", "role": roleForGroups(cfg, nil)})
			return
		}
		if cloudflare != nil {
			identity, err := cloudflare.identity(r)
			if err == nil && identity.Service {
				err = errInvalidCloudflareAccess
			}
			if err == nil {
				if revoked, checkErr := sessions.store.PrincipalRevoked(r.Context(), identity.Subject); checkErr != nil || revoked {
					err = store.ErrNotFound
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated":             err == nil,
				"auth_mode":                 config.BrowserAuthCloudflareAccess,
				"oidc_enabled":              false,
				"cloudflare_access_enabled": true,
				"identity":                  identityActor(identity),
				"role":                      roleForGroups(cfg, identity.Groups),
				"logout_url":                "/cdn-cgi/access/logout",
			})
			return
		}
		identity, valid := sessions.identity(r.Context(), r)
		if valid {
			if revoked, err := sessions.store.PrincipalRevoked(r.Context(), identity.Subject); err != nil || revoked {
				valid = false
			}
		}
		role := service.Role("")
		if valid {
			role = roleForGroups(cfg, identity.Groups)
		}
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": valid, "auth_mode": config.BrowserAuthOIDC, "oidc_enabled": cfg.OIDCEnabled(), "cloudflare_access_enabled": false, "identity": identityActor(identity), "role": role})
	}
}

func apiError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status := http.StatusInternalServerError
	message := "internal error"
	switch {
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
		message = "not found"
	case errors.Is(err, store.ErrConflict):
		status = http.StatusConflict
		message = "conflict"
	case errors.Is(err, service.ErrConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrForbidden):
		status = http.StatusForbidden
		message = "forbidden"
	case errors.Is(err, service.ErrRateLimit):
		status = http.StatusTooManyRequests
		message = err.Error()
	case errors.Is(err, service.ErrValidation):
		status = http.StatusBadRequest
		message = err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
	return true
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !requireJSONContentType(w, r) {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	return true
}

func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func principal(ctx context.Context) service.Principal {
	value, _ := ctx.Value(principalKey{}).(service.Principal)
	return value
}
func actor(ctx context.Context) string { return principal(ctx).ID }
func mcpPrincipal(ctx context.Context) service.Principal {
	if authenticated := principal(ctx); authenticated.Agent && authenticated.ID != "" {
		switch authenticated.ID {
		case "agent":
			authenticated.ID = "agent:shared"
		case "local":
			authenticated.ID = "agent:local"
		}
		return authenticated
	}
	switch authenticated := strings.TrimSpace(actor(ctx)); authenticated {
	case "", "agent":
		return service.AgentPrincipal("agent:shared")
	case "local":
		return service.AgentPrincipal("agent:local")
	default:
		return service.AgentPrincipal(authenticated)
	}
}
func mcpClient(request *mcp.CallToolRequest) string {
	if request == nil || request.ClientInfo() == nil {
		return ""
	}
	name := cleanClientMetadata(request.ClientInfo().Name)
	version := cleanClientMetadata(request.ClientInfo().Version)
	client := name
	if name != "" && version != "" {
		client += "/" + version
	}
	if len(client) > 100 {
		client = strings.ToValidUTF8(client[:100], "")
	}
	return client
}
func cleanClientMetadata(value string) string {
	return strings.Map(func(character rune) rune {
		if character < ' ' || character == '\u007f' {
			return -1
		}
		return character
	}, strings.TrimSpace(value))
}
func secureEqual(left, right string) bool {
	if left == "" || right == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
func sameOrigin(origin, host string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == host && parsed.User == nil
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func safeBrowserMutation(r *http.Request) bool {
	fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if fetchSite == "cross-site" {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" && !strings.EqualFold(origin, "null") {
		return sameOrigin(origin, r.Host)
	}
	return fetchSite == "same-origin"
}
func requireAllowedHost(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if len(allowed) > 0 && !hostAllowed(r.Host, allowed) {
			writeJSON(w, http.StatusMisdirectedRequest, map[string]string{"error": "request host is not allowed"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func hostAllowed(host string, allowed []string) bool {
	wanted := strings.ToLower(strings.TrimSpace(host))
	hostname := strings.Trim(wanted, "[]")
	if parsed, _, err := net.SplitHostPort(wanted); err == nil {
		hostname = strings.Trim(parsed, "[]")
	}
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == wanted || (!strings.Contains(candidate, ":") && candidate == hostname) {
			return true
		}
	}
	return false
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; manifest-src 'self'; object-src 'none'; script-src 'self'; style-src 'self'; worker-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func logging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
func spaHandler(files http.Handler, assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			files.ServeHTTP(w, r2)
			return
		}
		files.ServeHTTP(w, r)
	})
}
