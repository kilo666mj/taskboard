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
}

func TestSavePushSubscriptionAcceptsExpirationTime(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "public-key", "private-key", "mailto:test@example.com", logger)
	body := []byte(`{"endpoint":"https://web.push.apple.com/test","expirationTime":null,"keys":{"p256dh":"test-p256dh","auth":"test-auth"}}`)
	request := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/push/subscriptions", bytes.NewReader(body))
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

func TestRESTCreatesAndListsMultipleTasks(t *testing.T) {
	tasks, database, logger := serverFixture(t)
	notifications := push.New(database, tasks, "", "", "", logger)
	handler, err := New(config.Config{AllowInsecure: true, LeaseDuration: time.Minute}, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"First task", "Second task"} {
		body, _ := json.Marshal(map[string]any{"title": title, "checklist": []string{"One", "Two"}})
		request := httptest.NewRequest(http.MethodPost, "http://taskboard/api/v1/tasks", bytes.NewReader(body))
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
	handler, err := New(config.Config{AllowInsecure: true, LeaseDuration: time.Minute}, database, tasks, notifications, logger)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://taskboard/api/v1/tasks/capture", strings.NewReader(`{"title":"Inbox item","section":"Personal"}`))
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
	tasks, _, _ := serverFixture(t)
	response := newFlushingRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodGet, "http://taskboard/api/v1/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		eventsWithKeepalive(tasks, time.Millisecond).ServeHTTP(response, request)
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
	cfg := config.Config{AuthToken: strings.Repeat("test-token-", 4), LeaseDuration: time.Minute, OIDCIssuer: "https://idp.example.com", OIDCClientID: "taskboard", OIDCRedirectURL: "https://taskboard.example.com/api/v1/auth/oidc/callback"}
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
	confirmation, err := database.CreateDesktopHandoff(t.Context(), code, store.BrowserIdentity{Subject: "subject-1", Email: "person@example.com"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	unconfirmed := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	unconfirmed.Header.Set("Origin", "https://taskboard.example.com")
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
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(response.Result().Cookies()) != 1 {
		t.Fatalf("exchange status/cookies = %d/%d, body=%s", response.Code, len(response.Result().Cookies()), response.Body.String())
	}

	replay := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	replay.Header.Set("Origin", "https://taskboard.example.com")
	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, replay)
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", replayed.Code)
	}

	crossOrigin := httptest.NewRequest(http.MethodPost, "https://taskboard.example.com/api/v1/auth/desktop/session", bytes.NewReader(body))
	crossOrigin.Header.Set("Origin", "https://evil.example")
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
	confirmation, err := database.CreateDesktopHandoff(t.Context(), code, store.BrowserIdentity{Subject: "subject-1"}, time.Minute)
	if err != nil {
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
	exchanged := httptest.NewRecorder()
	handler.ServeHTTP(exchanged, exchange)
	if exchanged.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled exchange status = %d, want 401", exchanged.Code)
	}
}
