package observability

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeDatabaseStats struct{}

func (fakeDatabaseStats) Stats() sql.DBStats {
	return sql.DBStats{MaxOpenConnections: 10, OpenConnections: 3, InUse: 2, Idle: 1}
}

func TestMetricsExposeBoundedLabelsAndOperationalOutcomes(t *testing.T) {
	metrics := New(fakeDatabaseStats{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tasks/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/01M2SECRET", nil)
	metrics.HTTP(mux).ServeHTTP(httptest.NewRecorder(), request)

	metrics.ObserveAuth("token", "success")
	metrics.ObserveDatabasePing(errors.New("unavailable"))
	metrics.ObserveAgentRun("heartbeat", nil)
	metrics.AddStaleRuns(2)
	metrics.AddSubscriber()
	metrics.ObserveEvent("dropped")
	metrics.ObservePush("error")

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	wants := []string{
		`taskboard_http_requests_total{method="GET",route="GET /api/v1/tasks/{id}",status_class="2xx"} 1`,
		`taskboard_auth_attempts_total{mechanism="token",outcome="success"} 1`,
		`taskboard_database_ping_total{outcome="error"} 1`,
		`taskboard_database_connections{state="in_use"} 2`,
		`taskboard_agent_run_operations_total{operation="heartbeat",outcome="success"} 1`,
		`taskboard_agent_run_operations_total{operation="sweep",outcome="stale"} 2`,
		`taskboard_event_subscribers 1`,
		`taskboard_event_deliveries_total{outcome="dropped"} 1`,
		`taskboard_push_deliveries_total{outcome="error"} 1`,
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("metrics output does not contain %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "01M2SECRET") {
		t.Fatal("raw task ID leaked into a metric label")
	}
}
