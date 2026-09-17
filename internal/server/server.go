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
	Statuses []model.TaskStatus `json:"statuses,omitempty" jsonschema:"Optional task statuses to include"`
	Limit    int                `json:"limit,omitempty" jsonschema:"Maximum tasks, default 100 and maximum 200"`
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
type moveInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ULID"`
	model.MoveRequest
}
type tasksOutput struct {
	Tasks []model.Task `json:"tasks"`
}
type taskOutput struct {
	Task model.Task `json:"task"`
}
type runOutput struct {
	Run model.AgentRun `json:"run"`
}
type templatesOutput struct {
	Templates []model.Template `json:"templates"`
}
type templateOutput struct {
	Template model.Template `json:"template"`
}

func New(cfg config.Config, database *store.Store, service *service.Service, notifications *push.Service, logger *slog.Logger) (http.Handler, error) {
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
	authenticated := auth(cfg, sessions, cloudflare, logger)
	mux.Handle("/mcp", authenticated(mcpHandler))
	mux.Handle("/mcp/", authenticated(mcpHandler))
	mux.Handle("GET /api/v1/tasks", authenticated(http.HandlerFunc(listTasks(service))))
	mux.Handle("POST /api/v1/tasks", authenticated(http.HandlerFunc(startTask(service))))
	mux.Handle("POST /api/v1/tasks/capture", authenticated(http.HandlerFunc(createTask(service))))
	mux.Handle("GET /api/v1/tasks/{id}", authenticated(http.HandlerFunc(getTask(service))))
	mux.Handle("PATCH /api/v1/tasks/{id}", authenticated(http.HandlerFunc(updateTask(service))))
	mux.Handle("POST /api/v1/tasks/{id}/runs", authenticated(http.HandlerFunc(claimTask(service))))
	mux.Handle("POST /api/v1/tasks/{id}/move", authenticated(http.HandlerFunc(moveTask(service))))
	mux.Handle("POST /api/v1/tasks/{id}/runs/{run}/heartbeat", authenticated(http.HandlerFunc(heartbeat(service))))
	mux.Handle("GET /api/v1/events", authenticated(http.HandlerFunc(events(service, newEventStreamLimiter(128, 4)))))
	mux.Handle("GET /api/v1/templates", authenticated(http.HandlerFunc(listTemplates(service))))
	mux.Handle("POST /api/v1/templates", authenticated(http.HandlerFunc(saveTemplate(service))))
	mux.Handle("DELETE /api/v1/templates/{id}", authenticated(http.HandlerFunc(deleteTemplate(service))))
	mux.Handle("/pwa-kit/", pwakit.Handler())
	mux.Handle("GET /api/v1/push/key", authenticated(http.HandlerFunc(pushKey(notifications))))
	mux.Handle("POST /api/v1/push/subscriptions", authenticated(http.HandlerFunc(savePushSubscription(notifications))))
	mux.Handle("PATCH /api/v1/push/subscriptions", authenticated(http.HandlerFunc(updatePushPreferences(notifications))))
	mux.Handle("DELETE /api/v1/push/subscriptions", authenticated(http.HandlerFunc(deletePushSubscription(notifications))))
	mux.HandleFunc("GET /api/v1/session", sessionState(cfg, sessions, cloudflare))
	mux.HandleFunc("DELETE /api/v1/session", logout(cfg, sessions))
	mux.HandleFunc("POST /api/v1/auth/desktop/session", desktopSessionExchange(sessions))
	mux.HandleFunc("GET /api/v1/auth/desktop/complete", desktopLoginComplete(sessions))
	mux.HandleFunc("POST /api/v1/auth/desktop/confirm", desktopConfirm(sessions))
	mux.HandleFunc("POST /api/v1/auth/desktop/cancel", desktopCancel(sessions))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "taskboard", "version": Version})
	})
	mux.Handle("GET /readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := service.Ready(ctx); err != nil {
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
	return securityHeaders(logging(logger, requireAllowedHost(cfg.AllowedHosts, mux))), nil
}

func pushKey(notifications *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !notifications.Enabled() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Web Push is not configured"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"public_key": notifications.PublicKey()})
	}
}

func savePushSubscription(notifications *push.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		Instructions: "Use task_create to capture future work without beginning execution. Before substantive agent work, start or claim the task, keep its task and run IDs, update it at meaningful transitions, heartbeat during long work, record blockers immediately, and complete only after all required checklist items are done or skipped with a reason.",
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_start", Description: "Register substantial work before beginning. Creates a durable task, checklist, and leased agent run; keep the returned task_id and run_id for updates.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input model.StartRequest) (*mcp.CallToolResult, model.StartResult, error) {
		if input.Type == "" {
			input.Type = defaultTaskType
		}
		result, err := tasks.StartFor(ctx, input, mcpPrincipal(ctx, request))
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_create", Description: "Capture future work without starting execution. Creates a queued task with no agent run; title is the only required field.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input model.CreateRequest) (*mcp.CallToolResult, taskOutput, error) {
		if input.Type == "" {
			input.Type = defaultTaskType
		}
		task, err := tasks.CreateFor(ctx, input, mcpPrincipal(ctx, request))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_update", Description: "Atomically update assignment, section, checklist progress, and user-visible status. expected_version prevents overwriting another agent's changes.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input updateInput) (*mcp.CallToolResult, taskOutput, error) {
		task, err := tasks.UpdateFor(ctx, input.TaskID, input.UpdateRequest, mcpPrincipal(ctx, request))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_claim", Description: "Claim an existing non-terminal task and receive a fresh leased run. Use this for handoff or to resume a task after its prior run became stale.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input claimInput) (*mcp.CallToolResult, model.StartResult, error) {
		result, err := tasks.ClaimFor(ctx, input.TaskID, input.ClaimRequest, mcpPrincipal(ctx, request))
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_move", Description: "Move an open task up or down within its section while preserving optimistic version checks.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input moveInput) (*mcp.CallToolResult, taskOutput, error) {
		task, err := tasks.MoveFor(ctx, input.TaskID, input.MoveRequest, mcpPrincipal(ctx, request))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_complete", Description: "Mark a task done. Rejected while any required checklist item remains open; skipped items require a recorded reason.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input updateInput) (*mcp.CallToolResult, taskOutput, error) {
		input.Status = model.TaskDone
		task, err := tasks.UpdateFor(ctx, input.TaskID, input.UpdateRequest, mcpPrincipal(ctx, request))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_heartbeat", Description: "Renew an active run lease during long work. The harness should call this automatically; expired runs become stale.", Annotations: mcpkit.Mutating(true, false)}, func(ctx context.Context, request *mcp.CallToolRequest, input heartbeatInput) (*mcp.CallToolResult, runOutput, error) {
		run, err := tasks.HeartbeatFor(ctx, input.TaskID, input.RunID, mcpPrincipal(ctx, request))
		return nil, runOutput{Run: run}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_get", Description: "Get one task with its ordered checklist and agent runs.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input taskIDInput) (*mcp.CallToolResult, taskOutput, error) {
		task, err := tasks.GetFor(ctx, input.TaskID, mcpPrincipal(ctx, request))
		return nil, taskOutput{Task: task}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_list", Description: "List agent-pickup work and team work explicitly assigned to this agent, optionally filtered by status.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, request *mcp.CallToolRequest, input listInput) (*mcp.CallToolResult, tasksOutput, error) {
		items, err := tasks.ListFor(ctx, input.Statuses, input.Limit, mcpPrincipal(ctx, request))
		return nil, tasksOutput{Tasks: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_template_list", Description: "List reusable Taskboard task and checklist templates.", Annotations: mcpkit.ReadOnly(false)}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, templatesOutput, error) {
		items, err := tasks.ListTemplates(ctx)
		return nil, templatesOutput{Templates: items}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "task_template_save", Description: "Create or replace a reusable task and checklist template by name.", Annotations: mcpkit.Mutating(false, false)}, func(ctx context.Context, _ *mcp.CallToolRequest, input model.TemplateRequest) (*mcp.CallToolResult, templateOutput, error) {
		item, err := tasks.SaveTemplate(ctx, input)
		return nil, templateOutput{Template: item}, err
	})
	return server
}

func listTemplates(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := tasks.ListTemplates(r.Context())
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
		item, err := tasks.SaveTemplate(r.Context(), input)
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, templateOutput{Template: item})
	}
}
func deleteTemplate(tasks *service.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiError(w, tasks.DeleteTemplate(r.Context(), r.PathValue("id"))) {
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
		items, err := tasks.ListFor(r.Context(), statuses, limit, principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, tasksOutput{Tasks: items})
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
		writeJSON(w, http.StatusOK, taskOutput{Task: task})
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
		run, err := tasks.HeartbeatFor(r.Context(), r.PathValue("id"), r.PathValue("run"), principal(r.Context()))
		if apiError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, runOutput{Run: run})
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

func events(tasks *service.Service, limiter *eventStreamLimiter) http.HandlerFunc {
	return eventsWithKeepalive(tasks, eventKeepaliveInterval, limiter)
}

func eventsWithKeepalive(tasks *service.Service, keepaliveInterval time.Duration, limiter *eventStreamLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principalID := principal(r.Context()).ID
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

func auth(cfg config.Config, sessions *browserSessions, cloudflare *cloudflareAccess, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mcpRequest := r.URL.Path == "/mcp" || strings.HasPrefix(r.URL.Path, "/mcp/")
			if !mcpRequest && !safeMethod(r.Method) && !safeBrowserMutation(r) {
				logger.Warn("rejected unsafe browser mutation")
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
				return
			}
			if cfg.AllowInsecure && cfg.AuthToken == "" {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, service.Principal{ID: "local", Agent: mcpRequest})))
				return
			}
			valid, who := false, ""
			if mcpRequest {
				values := r.Header.Values("Authorization")
				accessValues := r.Header.Values(cloudflareAccessJWTHeader)
				if len(values) > 0 && len(accessValues) > 0 {
					logger.Warn("rejected request with ambiguous credentials")
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
					return
				}
				if cloudflare != nil && len(accessValues) > 0 {
					if identity, err := cloudflare.identity(r); err == nil {
						valid = true
						who = identity.Subject
					}
				} else if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") {
					presented := strings.TrimSpace(strings.TrimPrefix(values[0], "Bearer "))
					valid = secureEqual(presented, cfg.AuthToken)
					who = "agent"
				}
			} else if cloudflare != nil {
				if identity, err := cloudflare.identity(r); err == nil {
					valid = true
					who = identity.Subject
				}
			} else if identity, ok := sessions.identity(r.Context(), r); ok {
				valid = true
				who = identity.Subject
			}
			if !valid {
				w.Header().Set("WWW-Authenticate", `Bearer realm="taskboard"`)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, service.Principal{ID: who, Agent: mcpRequest})))
		})
	}
}

func sessionState(cfg config.Config, sessions *browserSessions, cloudflare *cloudflareAccess) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.AllowInsecure && cfg.AuthToken == "" {
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "auth_mode": "local", "oidc_enabled": cfg.OIDCEnabled(), "cloudflare_access_enabled": false, "identity": "local"})
			return
		}
		if cloudflare != nil {
			identity, err := cloudflare.identity(r)
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated":             err == nil,
				"auth_mode":                 config.BrowserAuthCloudflareAccess,
				"oidc_enabled":              false,
				"cloudflare_access_enabled": true,
				"identity":                  identityActor(identity),
				"logout_url":                "/cdn-cgi/access/logout",
			})
			return
		}
		identity, valid := sessions.identity(r.Context(), r)
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": valid, "auth_mode": config.BrowserAuthOIDC, "oidc_enabled": cfg.OIDCEnabled(), "cloudflare_access_enabled": false, "identity": identityActor(identity)})
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
func mcpActor(ctx context.Context, request *mcp.CallToolRequest) string {
	if authenticated := actor(ctx); authenticated != "" && authenticated != "agent" {
		return authenticated
	}
	if client := request.ClientInfo(); client != nil && strings.TrimSpace(client.Name) != "" {
		return strings.TrimSpace(client.Name)
	}
	return actor(ctx)
}
func mcpPrincipal(ctx context.Context, request *mcp.CallToolRequest) service.Principal {
	return service.AgentPrincipal(mcpActor(ctx, request))
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
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	return sameOrigin(r.Header.Get("Origin"), r.Host)
}
func requireAllowedHost(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
