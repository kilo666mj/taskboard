package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kilo666mj/mcpkit/mcpkittest"
	"github.com/kilo666mj/oidcrp"
	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/push"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serverFixture(t *testing.T) (*service.Service, *store.Store, *slog.Logger) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	return service.New(database, time.Minute), database, slog.New(slog.NewTextHandler(io.Discard, nil))
}

func connectMCPAs(t *testing.T, server *mcp.Server, name, version string) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: name, Version: version}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Close()
	})
	return clientSession
}

func TestMCPToolSurfaceIsAnnotated(t *testing.T) {
	tasks, _, logger := serverFixture(t)
	session := mcpkittest.Connect(t, newMCPServer(tasks, model.TaskWork, logger))
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"task_claim", "task_complete", "task_create", "task_get", "task_heartbeat", "task_list", "task_move", "task_start", "task_template_list", "task_template_save", "task_update"}
	got := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil {
			t.Errorf("tool %q has no annotations", tool.Name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestMCPDefaultsNewTasksToConfiguredType(t *testing.T) {
	tasks, _, logger := serverFixture(t)
	session := mcpkittest.Connect(t, newMCPServer(tasks, model.TaskWork, logger))
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "task_create", Arguments: map[string]any{"title": "Agent task"}}); err != nil {
		t.Fatal(err)
	}
	items, err := tasks.List(t.Context(), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Type != model.TaskWork || items[0].Visibility != model.VisibilityAgent {
		t.Fatalf("created tasks = %+v", items)
	}
}

func TestMCPClientNameCannotSpoofCanonicalActor(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	session := connectMCPAs(t, newMCPServer(tasks, model.TaskWork, logger), "victim@example.com", "9.4")
	_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "task_start", Arguments: map[string]any{
		"title":     "Spoof-resistant task",
		"checklist": []string{"Verify attribution"},
		"agent":     "admin@example.com",
		"client":    "forged-client",
	}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := tasks.List(t.Context(), nil, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("list tasks = %+v, %v", items, err)
	}
	task, err := tasks.Get(t.Context(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.CreatedBy != "agent:shared" || task.Owner != "agent:shared" {
		t.Fatalf("task attribution = creator %q, owner %q", task.CreatedBy, task.Owner)
	}
	if len(task.Runs) != 1 || task.Runs[0].Agent != "agent:shared" || task.Runs[0].Client != "victim@example.com/9.4" {
		t.Fatalf("run attribution = %+v", task.Runs)
	}
	var eventActor string
	if err := database.DB().QueryRowContext(t.Context(), `SELECT actor FROM events WHERE task_id=? AND kind='task.started'`, task.ID).Scan(&eventActor); err != nil {
		t.Fatal(err)
	}
	if eventActor != "agent:shared" {
		t.Fatalf("event actor = %q, want agent:shared", eventActor)
	}
}

func TestHumanCanRenameRunThroughAPIWithoutChangingAttribution(t *testing.T) {
	tasks, _, _ := serverFixture(t)
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Named session", Checklist: []string{"Work"}}, service.AgentPrincipal("agent:worker"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(model.RenameRunRequest{ExpectedVersion: started.Task.Version, Callsign: "Maple Fox"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/tasks/task/runs/run", bytes.NewReader(body))
	request.SetPathValue("id", started.Task.ID)
	request.SetPathValue("run", started.Run.ID)
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), principalKey{}, service.HumanPrincipal("operator@example.com")))
	response := httptest.NewRecorder()
	renameRun(tasks).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var result taskOutput
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.Runs[0].Callsign != "Maple Fox" || result.Task.Runs[0].Agent != "agent:worker" {
		t.Fatalf("renamed run = %+v", result.Task.Runs[0])
	}
}

func TestMCPPrincipalUsesOnlyTrustedAuthenticationContext(t *testing.T) {
	for _, test := range []struct {
		principal service.Principal
		want      string
	}{
		{want: "agent:shared"},
		{principal: service.AgentPrincipal("agent"), want: "agent:shared"},
		{principal: service.AgentPrincipal("local"), want: "agent:local"},
		{principal: service.AgentPrincipal("cloudflare_access:service_token:build-bot"), want: "cloudflare_access:service_token:build-bot"},
	} {
		ctx := context.WithValue(t.Context(), principalKey{}, test.principal)
		if got := mcpPrincipal(ctx); got.ID != test.want || !got.Agent {
			t.Errorf("mcpPrincipal(%q) = %+v, want agent %q", test.principal.ID, got, test.want)
		}
	}
}

func TestBearerAuthenticationUsesSharedCanonicalAgent(t *testing.T) {
	_, database, logger := serverFixture(t)
	token := strings.Repeat("shared-token-", 3)
	handler := auth(config.Config{AuthToken: token}, newBrowserSessions(database, true, logger), nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := principal(r.Context())
		if got.ID != "agent:shared" || !got.Agent {
			t.Errorf("principal = %+v, want shared agent", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestSameOriginUsesExactHost(t *testing.T) {
	for _, test := range []struct {
		origin string
		host   string
		want   bool
	}{
		{"https://taskboard.example.com", "taskboard.example.com", true},
		{"http://127.0.0.1:8095", "127.0.0.1:8095", true},
		{"https://evil.example/?next=://taskboard.example.com", "taskboard.example.com", false},
		{"https://evil-taskboard.example.com", "taskboard.example.com", false},
		{"ftp://taskboard.example.com", "taskboard.example.com", false},
	} {
		if got := sameOrigin(test.origin, test.host); got != test.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", test.origin, test.host, got, test.want)
		}
	}
}

func TestAllowedHostUsesExactHostname(t *testing.T) {
	allowed := []string{"taskboard.example.com", "127.0.0.1:8095", "[2001:db8::1]:8095"}
	for _, test := range []struct {
		host string
		want bool
	}{
		{"taskboard.example.com", true},
		{"taskboard.example.com:443", true},
		{"TASKBOARD.EXAMPLE.COM", true},
		{"taskboard.example.com.evil.example", false},
		{"127.0.0.1:8095", true},
		{"127.0.0.1:8080", false},
		{"[2001:db8::1]:8095", true},
	} {
		if got := hostAllowed(test.host, allowed); got != test.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", test.host, got, test.want)
		}
	}
}

func TestSecurityHeadersIncludeContentPolicy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	securityHeaders(next).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/", nil))

	policy := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(policy, directive) {
			t.Errorf("Content-Security-Policy %q does not contain %q", policy, directive)
		}
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("application emitted ingress-owned HSTS header %q", got)
	}
	apiResponse := httptest.NewRecorder()
	securityHeaders(next).ServeHTTP(apiResponse, httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/admin/export", nil))
	if got := apiResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("API Cache-Control = %q, want no-store", got)
	}
}

func TestDesktopConfirmationPageEscapesAllValues(t *testing.T) {
	response := httptest.NewRecorder()
	desktopConfirmationPage(response, http.StatusOK, `<script>alert("title")</script>`, `<img src=x onerror=alert("message")>`, `<svg onload=alert("code")>`)
	body := response.Body.String()
	for _, unsafe := range []string{"<script", "<img", "<svg"} {
		if strings.Contains(body, unsafe) {
			t.Fatalf("confirmation page contains unescaped value %q: %s", unsafe, body)
		}
	}
	for _, escaped := range []string{"&lt;script", "&lt;img", "&lt;svg"} {
		if !strings.Contains(body, escaped) {
			t.Fatalf("confirmation page missing escaped value %q: %s", escaped, body)
		}
	}
}

func TestRequestRateLimiterRejectsBurst(t *testing.T) {
	limiter := newRequestRateLimiter(2, time.Minute, 2)
	now := time.Now()
	for attempt := 1; attempt <= 2; attempt++ {
		if !limiter.allow("client", now) {
			t.Fatalf("rate limiter rejected allowed attempt %d", attempt)
		}
	}
	if limiter.allow("client", now) {
		t.Fatal("rate limiter accepted a request above its fixed-window bound")
	}
	if !limiter.allow("client", now.Add(time.Minute)) {
		t.Fatal("rate limiter did not reset after its window")
	}
	if !limiter.allow("second", now) || !limiter.allow("third", now) {
		t.Fatal("rate limiter did not retain bounded overflow capacity")
	}
	if len(limiter.entries) > limiter.maxEntries {
		t.Fatalf("rate limiter retained %d entries, want at most %d", len(limiter.entries), limiter.maxEntries)
	}
}

func TestAuthenticationFailuresAreRateLimited(t *testing.T) {
	_, database, _ := serverFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, true, logger)
	handler := auth(config.Config{AuthToken: strings.Repeat("token", 8)}, sessions, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for attempt := 1; attempt <= 31; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/tasks", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 31 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestAuthenticatedMutationsAreRateLimited(t *testing.T) {
	_, database, _ := serverFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := newBrowserSessions(database, true, logger)
	token := strings.Repeat("token", 8)
	handler := auth(config.Config{AuthToken: token}, sessions, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for attempt := 1; attempt <= 301; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/mcp", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if attempt == 301 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestDesktopSessionExchangeIsRateLimited(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	handler, err := New(config.Config{AllowInsecure: true, DefaultRole: "member", LeaseDuration: time.Minute}, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 11; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "http://taskboard/api/v1/auth/desktop/session", strings.NewReader(`{"code":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`))
		request.Header.Set("Origin", "http://taskboard")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 11 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestAllowedHostExemptsOnlyHealthProbes(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := requireAllowedHost([]string{"taskboard.example.com"}, next)
	for _, path := range []string{"/healthz", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, "http://10.0.0.8"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("probe %s status = %d, want 204", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://10.0.0.8/api/v1/tasks", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("application status = %d, want 421", response.Code)
	}
}

func TestSavePushSubscriptionAcceptsExpirationTime(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "public-key", "private-key", "mailto:test@example.com", logger)
	body := []byte(`{"endpoint":"https://web.push.apple.com/test","expirationTime":null,"keys":{"p256dh":"test-p256dh","auth":"test-auth"}}`)
	request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/push/subscriptions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	savePushSubscription(notifications).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("save status = %d, body=%s", response.Code, response.Body.String())
	}
	subscriptions, err := database.ListPushSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 1 || subscriptions[0].Endpoint != "https://web.push.apple.com/test" {
		t.Fatalf("subscriptions = %#v", subscriptions)
	}
	preferences := httptest.NewRequest(http.MethodPatch, "https://taskboard.example.com/api/v1/push/subscriptions", strings.NewReader(`{"endpoint":"https://web.push.apple.com/test","progress":false,"reminders":true,"summaries":true}`))
	preferences.Header.Set("Content-Type", "application/json")
	preferenceResponse := httptest.NewRecorder()
	updatePushPreferences(notifications).ServeHTTP(preferenceResponse, preferences)
	if preferenceResponse.Code != http.StatusNoContent {
		t.Fatalf("preferences status = %d, body=%s", preferenceResponse.Code, preferenceResponse.Body.String())
	}
	subscriptions, err = database.ListPushSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if subscriptions[0].NotifyProgress || !subscriptions[0].NotifyReminders || !subscriptions[0].NotifySummaries {
		t.Fatalf("subscription preferences = %#v", subscriptions[0])
	}
}

func TestPushSubscriptionEndpointCannotBeTakenOver(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "public-key", "private-key", "mailto:test@example.com", logger)
	requestFor := func(subject, body string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/push/subscriptions", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		return request.WithContext(context.WithValue(request.Context(), principalKey{}, service.HumanPrincipal(subject)))
	}
	aliceBody := `{"endpoint":"https://web.push.apple.com/device","keys":{"p256dh":"alice-key","auth":"alice-auth"}}`
	response := httptest.NewRecorder()
	savePushSubscription(notifications).ServeHTTP(response, requestFor("alice@example.com", aliceBody))
	if response.Code != http.StatusNoContent {
		t.Fatalf("alice save status = %d, body=%s", response.Code, response.Body.String())
	}

	bobBody := `{"endpoint":"https://web.push.apple.com/device","keys":{"p256dh":"bob-key","auth":"bob-auth"}}`
	response = httptest.NewRecorder()
	savePushSubscription(notifications).ServeHTTP(response, requestFor("bob@example.com", bobBody))
	if response.Code != http.StatusConflict {
		t.Fatalf("bob save status = %d, body=%s", response.Code, response.Body.String())
	}
	subscriptions, err := database.ListPushSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 1 || subscriptions[0].OwnerID != "alice@example.com" || subscriptions[0].P256DH != "alice-key" || subscriptions[0].Auth != "alice-auth" {
		t.Fatalf("subscription was replaced: %#v", subscriptions)
	}

	preferences := httptest.NewRequest(http.MethodPatch, "https://taskboard.example.com/api/v1/push/subscriptions", strings.NewReader(`{"endpoint":"https://web.push.apple.com/device","progress":false}`))
	preferences.Header.Set("Content-Type", "application/json")
	preferences = preferences.WithContext(context.WithValue(preferences.Context(), principalKey{}, service.HumanPrincipal("bob@example.com")))
	response = httptest.NewRecorder()
	updatePushPreferences(notifications).ServeHTTP(response, preferences)
	if response.Code != http.StatusNotFound {
		t.Fatalf("bob preferences status = %d, body=%s", response.Code, response.Body.String())
	}

	remove := httptest.NewRequest(http.MethodDelete, "https://taskboard.example.com/api/v1/push/subscriptions", strings.NewReader(`{"endpoint":"https://web.push.apple.com/device"}`))
	remove.Header.Set("Content-Type", "application/json")
	remove = remove.WithContext(context.WithValue(remove.Context(), principalKey{}, service.HumanPrincipal("bob@example.com")))
	response = httptest.NewRecorder()
	deletePushSubscription(notifications).ServeHTTP(response, remove)
	if response.Code != http.StatusNoContent {
		t.Fatalf("bob delete status = %d, body=%s", response.Code, response.Body.String())
	}
	subscriptions, err = database.ListPushSubscriptions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 1 || subscriptions[0].OwnerID != "alice@example.com" {
		t.Fatalf("subscription was deleted: %#v", subscriptions)
	}
}

func TestRESTCreatesAndListsMultipleTasks(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	handler, err := New(config.Config{AllowInsecure: true, DefaultRole: "member", LeaseDuration: time.Minute}, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"First task", "Second task"} {
		body, _ := json.Marshal(map[string]any{"title": title, "checklist": []string{"One", "Two"}})
		request := httptest.NewRequest(http.MethodPost, "http://taskboard/api/v1/tasks", bytes.NewReader(body))
		request.Header.Set("Origin", "http://taskboard")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create status = %d, body=%s", response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://taskboard/api/v1/tasks", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d", response.Code)
	}
	var payload tasksOutput
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(payload.Tasks))
	}
}

func TestRESTCapturesTitleOnlyTask(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	handler, err := New(config.Config{AllowInsecure: true, DefaultRole: "member", LeaseDuration: time.Minute}, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://taskboard/api/v1/tasks/capture", strings.NewReader(`{"title":"Inbox item","section":"Personal"}`))
	request.Header.Set("Origin", "http://taskboard")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("capture status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload taskOutput
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Task.Status != "queued" || payload.Task.Section != "Personal" || len(payload.Task.Items) != 0 || len(payload.Task.Runs) != 0 {
		t.Fatalf("captured task = %+v", payload.Task)
	}
}

func TestEventsExposeReadyAndKeepaliveSignals(t *testing.T) {
	tasks, database, _ := serverFixture(t)
	response := newFlushingRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodGet, "http://taskboard/api/v1/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		eventsWithKeepalive(tasks, database, time.Millisecond, newEventStreamLimiter(10, 10)).ServeHTTP(response, request)
		close(done)
	}()

	var body string
	for !strings.Contains(body, "event: ping") {
		select {
		case body = <-response.flushed:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("timed out waiting for event stream keepalive")
		}
	}
	cancel()
	<-done
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, "retry: 2000") || !strings.Contains(body, "event: ready") {
		t.Fatalf("stream did not contain retry and ready signals: %q", body)
	}
}

func TestEventStreamLimitsPerPrincipalAndGlobally(t *testing.T) {
	tasks, database, _ := serverFixture(t)
	limiter := newEventStreamLimiter(2, 1)
	handler := eventsWithKeepalive(tasks, database, time.Hour, limiter)
	type stream struct {
		cancel   context.CancelFunc
		done     chan struct{}
		response *flushingRecorder
	}
	open := func(subject string) stream {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		ctx = context.WithValue(ctx, principalKey{}, service.HumanPrincipal(subject))
		request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/events", nil).WithContext(ctx)
		response := newFlushingRecorder()
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(response, request)
			close(done)
		}()
		select {
		case <-response.flushed:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("timed out opening event stream")
		}
		return stream{cancel: cancel, done: done, response: response}
	}
	rejected := func(subject string) *httptest.ResponseRecorder {
		t.Helper()
		ctx := context.WithValue(t.Context(), principalKey{}, service.HumanPrincipal(subject))
		request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/events", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	alice := open("alice@example.com")
	defer func() { alice.cancel(); <-alice.done }()
	if response := rejected("alice@example.com"); response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("per-principal limit status/headers = %d/%v", response.Code, response.Header())
	}
	bob := open("bob@example.com")
	if response := rejected("charlie@example.com"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("global limit status = %d", response.Code)
	}
	bob.cancel()
	<-bob.done
	charlie := open("charlie@example.com")
	charlie.cancel()
	<-charlie.done
}

func TestEventStreamDisconnectsOffboardedPrincipal(t *testing.T) {
	tasks, database, _ := serverFixture(t)
	response := newFlushingRecorder()
	ctx := context.WithValue(t.Context(), principalKey{}, service.HumanPrincipal("departed@example.com"))
	request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		eventsWithKeepalive(tasks, database, time.Millisecond, newEventStreamLimiter(10, 10)).ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-response.flushed:
	case <-time.After(time.Second):
		t.Fatal("timed out opening event stream")
	}
	if err := database.OffboardPrincipal(t.Context(), "departed@example.com", "owner@example.com", "left workspace"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("offboarded principal's event stream remained connected")
	}
}

type failingRevocationChecker struct{}

func (failingRevocationChecker) PrincipalRevoked(context.Context, string) (bool, error) {
	return false, store.ErrNotFound
}

func TestEventStreamFailsClosedWhenRevocationCannotBeChecked(t *testing.T) {
	tasks, _, _ := serverFixture(t)
	ctx := context.WithValue(t.Context(), principalKey{}, service.HumanPrincipal("person@example.com"))
	request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/events", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	eventsWithKeepalive(tasks, failingRevocationChecker{}, time.Hour, newEventStreamLimiter(10, 10)).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revocation lookup failure status = %d, want 401", response.Code)
	}
}

func TestBrowserMutationsRequireSameOriginAndJSON(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	handler, err := New(config.Config{AllowInsecure: true, DefaultRole: "member", LeaseDuration: time.Minute}, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := func(origin, contentType, fetchSite string) *http.Request {
		result := httptest.NewRequest(http.MethodPost, "http://taskboard/api/v1/tasks/capture", strings.NewReader(`{"title":"Boundary test"}`))
		if origin != "" {
			result.Header.Set("Origin", origin)
		}
		if contentType != "" {
			result.Header.Set("Content-Type", contentType)
		}
		if fetchSite != "" {
			result.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		return result
	}
	for _, test := range []struct {
		name       string
		request    *http.Request
		wantStatus int
	}{
		{"missing origin", request("", "application/json", ""), http.StatusForbidden},
		{"cross origin", request("https://evil.example", "application/json", ""), http.StatusForbidden},
		{"cross-site metadata", request("http://taskboard", "application/json", "cross-site"), http.StatusForbidden},
		{"missing content type", request("http://taskboard", "", "same-origin"), http.StatusUnsupportedMediaType},
		{"wrong content type", request("http://taskboard", "text/plain", "same-origin"), http.StatusUnsupportedMediaType},
		{"same origin JSON", request("http://taskboard", "application/json; charset=utf-8", "same-origin"), http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestLogoutRequiresSameOrigin(t *testing.T) {
	_, database, logger := serverFixture(t)
	sessions := newBrowserSessions(database, true, logger)
	handler := logout(config.Config{}, sessions)

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "https://taskboard.example.com/api/v1/session", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing-origin logout status = %d", missing.Code)
	}

	request := httptest.NewRequest(http.MethodDelete, "https://taskboard.example.com/api/v1/session", nil)
	request.Header.Set("Origin", "https://taskboard.example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin logout status = %d", response.Code)
	}
}

type flushingRecorder struct {
	header  http.Header
	flushed chan string
	mu      sync.Mutex
	body    bytes.Buffer
}

func newFlushingRecorder() *flushingRecorder {
	return &flushingRecorder{header: make(http.Header), flushed: make(chan string, 4)}
}

func (r *flushingRecorder) Header() http.Header { return r.header }

func (r *flushingRecorder) WriteHeader(int) {}

func (r *flushingRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(data)
}

func (r *flushingRecorder) Flush() {
	r.mu.Lock()
	body := r.body.String()
	r.mu.Unlock()
	select {
	case r.flushed <- body:
	default:
	}
}

func TestIdentityBoundBrowserSession(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	token := strings.Repeat("test-token-", 4)
	notifications := push.New(database, tasks, "", "", "", logger)
	cfg := config.Config{AuthToken: token, LeaseDuration: time.Minute, OIDCIssuer: "https://idp.example.com", OIDCClientID: "taskboard", OIDCRedirectURL: "https://taskboard.example.com/api/v1/auth/oidc/callback"}
	handler, err := New(cfg, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://taskboard/api/v1/tasks", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	sessionToken, _, err := database.CreateBrowserSession(t.Context(), store.BrowserIdentity{Subject: "subject-1", Email: "person@example.com"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://taskboard/api/v1/tasks", nil)
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: sessionToken})
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("cookie session status = %d", authorized.Code)
	}

	bearerREST := httptest.NewRequest(http.MethodGet, "http://taskboard/api/v1/tasks", nil)
	bearerREST.Header.Set("Authorization", "Bearer "+token)
	bearerResponse := httptest.NewRecorder()
	handler.ServeHTTP(bearerResponse, bearerREST)
	if bearerResponse.Code != http.StatusUnauthorized {
		t.Fatalf("agent bearer on REST status = %d, want 401", bearerResponse.Code)
	}

	cookieMCP := httptest.NewRequest(http.MethodPost, "http://taskboard/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)))
	cookieMCP.Header.Set("Content-Type", "application/json")
	cookieMCP.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: sessionToken})
	cookieMCPResponse := httptest.NewRecorder()
	handler.ServeHTTP(cookieMCPResponse, cookieMCP)
	if cookieMCPResponse.Code != http.StatusUnauthorized {
		t.Fatalf("browser cookie on MCP status = %d, want 401", cookieMCPResponse.Code)
	}
}

func TestRESTEnforcesPrivateAndTeamVisibility(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), DefaultRole: "member", LeaseDuration: time.Minute, OIDCIssuer: "https://idp.example.com", OIDCClientID: "taskboard", OIDCRedirectURL: "https://taskboard.example.com/api/v1/auth/oidc/callback"}
	handler, err := New(cfg, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	newSession := func(email string) string {
		t.Helper()
		token, _, err := database.CreateBrowserSession(t.Context(), store.BrowserIdentity{Subject: email, Email: email}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	alice, bob := newSession("alice@example.com"), newSession("bob@example.com")
	create := func(session, title, visibility string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"title": title, "visibility": visibility})
		request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/tasks/capture", bytes.NewReader(body))
		request.Header.Set("Origin", "https://taskboard.example.com")
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: session})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %q status = %d, body=%s", title, response.Code, response.Body.String())
		}
	}
	list := func(session string) []model.Task {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/tasks?limit=200", nil)
		request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: session})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("list status = %d, body=%s", response.Code, response.Body.String())
		}
		var output tasksOutput
		if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
			t.Fatal(err)
		}
		return output.Tasks
	}

	create(alice, "Alice private", "private")
	if got := list(bob); len(got) != 0 {
		t.Fatalf("bob private list = %+v, want empty", got)
	}
	create(alice, "Team task", "team")
	got := list(bob)
	if len(got) != 1 || got[0].Title != "Team task" || got[0].Visibility != model.VisibilityTeam {
		t.Fatalf("bob team list = %+v", got)
	}
	if got := list(alice); len(got) != 2 {
		t.Fatalf("alice list length = %d, want 2", len(got))
	}
}

func TestDesktopSessionExchangeIsSingleUseAndSameOrigin(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), LeaseDuration: time.Minute, OIDCIssuer: "https://idp.example.com", OIDCClientID: "taskboard", OIDCRedirectURL: "https://taskboard.example.com/api/v1/auth/oidc/callback"}
	handler, err := New(cfg, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.Repeat("ab", 32)
	approval, err := oidcrp.NewDesktopConfirmation(code)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := approval.BrowserSecret
	if err := database.CreateDesktopHandoff(t.Context(), code, confirmation, approval.VerificationCode, store.BrowserIdentity{Subject: "subject-1", Email: "person@example.com"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	unconfirmed := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	unconfirmed.Header.Set("Origin", "https://taskboard.example.com")
	unconfirmed.Header.Set("Content-Type", "application/json")
	unconfirmedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unconfirmedResponse, unconfirmed)
	if unconfirmedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unconfirmed exchange status = %d, want 401", unconfirmedResponse.Code)
	}
	attackerConfirm := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/confirm", nil)
	attackerConfirm.Header.Set("Origin", "https://taskboard.example.com")
	attackerConfirm.AddCookie(&http.Cookie{Name: desktopConfirmCookie, Value: code, Path: "/api/v1/auth/desktop"})
	attackerConfirmation := httptest.NewRecorder()
	handler.ServeHTTP(attackerConfirmation, attackerConfirm)
	if attackerConfirmation.Code != http.StatusGone {
		t.Fatalf("attacker-known handoff confirmation status = %d, want 410", attackerConfirmation.Code)
	}

	confirmationCookie := &http.Cookie{Name: desktopConfirmCookie, Value: confirmation, Path: "/api/v1/auth/desktop"}
	crossOriginConfirm := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/confirm", nil)
	crossOriginConfirm.Header.Set("Origin", "https://evil.example")
	crossOriginConfirm.AddCookie(confirmationCookie)
	crossOriginConfirmation := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginConfirmation, crossOriginConfirm)
	if crossOriginConfirmation.Code != http.StatusForbidden {
		t.Fatalf("cross-origin confirmation status = %d, want 403", crossOriginConfirmation.Code)
	}
	confirmationPage := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/auth/desktop/complete", nil)
	confirmationPage.AddCookie(confirmationCookie)
	confirmationResponse := httptest.NewRecorder()
	handler.ServeHTTP(confirmationResponse, confirmationPage)
	if confirmationResponse.Code != http.StatusOK || !strings.Contains(confirmationResponse.Body.String(), desktopVerificationCode(code)) {
		t.Fatalf("confirmation page status/body = %d/%s", confirmationResponse.Code, confirmationResponse.Body.String())
	}
	confirm := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/confirm", nil)
	confirm.Header.Set("Origin", "https://taskboard.example.com")
	confirm.AddCookie(confirmationCookie)
	confirmed := httptest.NewRecorder()
	handler.ServeHTTP(confirmed, confirm)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirmation status = %d, body=%s", confirmed.Code, confirmed.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	request.Header.Set("Origin", "https://taskboard.example.com")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 1 {
		t.Fatalf("exchange status/cookies = %d/%d, body=%s", response.Code, len(response.Result().Cookies()), response.Body.String())
	}

	replay := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	replay.Header.Set("Origin", "https://taskboard.example.com")
	replay.Header.Set("Content-Type", "application/json")
	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, replay)
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", replayed.Code)
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	crossOrigin.Header.Set("Origin", "https://evil.example")
	crossOrigin.Header.Set("Content-Type", "application/json")
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, crossOrigin)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", rejected.Code)
	}
}

func TestIssueDesktopKeepsBrowserConfirmationSecretSeparate(t *testing.T) {
	_, database, logger := serverFixture(t)
	sessions := newBrowserSessions(database, true, logger)
	code := strings.Repeat("ef", 32)
	request := httptest.NewRequest(http.MethodGet, "https://taskboard.example.com/api/v1/auth/oidc/callback", nil)
	response := httptest.NewRecorder()
	if err := sessions.IssueDesktop(response, request, oidcrp.Identity{Subject: "subject-1"}, code); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != desktopConfirmCookie || cookies[0].Value == code || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("desktop confirmation cookie = %#v", cookies)
	}
	if verification, err := database.PendingDesktopHandoff(t.Context(), cookies[0].Value); err != nil || verification != desktopVerificationCode(code) {
		t.Fatalf("pending browser confirmation = %q, %v", verification, err)
	}
}

func TestDesktopSessionConfirmationCanBeCancelled(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), LeaseDuration: time.Minute, OIDCIssuer: "https://idp.example.com", OIDCClientID: "taskboard", OIDCRedirectURL: "https://taskboard.example.com/api/v1/auth/oidc/callback"}
	handler, err := New(cfg, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.Repeat("cd", 32)
	approval, err := oidcrp.NewDesktopConfirmation(code)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := approval.BrowserSecret
	if err := database.CreateDesktopHandoff(t.Context(), code, confirmation, approval.VerificationCode, store.BrowserIdentity{Subject: "subject-1"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	cancel := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/cancel", nil)
	cancel.Header.Set("Origin", "https://taskboard.example.com")
	cancel.AddCookie(&http.Cookie{Name: desktopConfirmCookie, Value: confirmation, Path: "/api/v1/auth/desktop"})
	cancelled := httptest.NewRecorder()
	handler.ServeHTTP(cancelled, cancel)
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body=%s", cancelled.Code, cancelled.Body.String())
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	exchange := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	exchange.Header.Set("Origin", "https://taskboard.example.com")
	exchange.Header.Set("Content-Type", "application/json")
	exchanged := httptest.NewRecorder()
	handler.ServeHTTP(exchanged, exchange)
	if exchanged.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled exchange status = %d, want 401", exchanged.Code)
	}
}
