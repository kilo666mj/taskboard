package observability

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/felixge/httpsnoop"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns Taskboard's private Prometheus registry. Labels are deliberately
// restricted to small, fixed vocabularies; task, user, agent, and endpoint IDs
// must never be used as label values.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	authAttempts *prometheus.CounterVec
	databasePing *prometheus.CounterVec
	agentRuns    *prometheus.CounterVec
	events       *prometheus.CounterVec
	subscribers  prometheus.Gauge
	push         *prometheus.CounterVec
}

type databaseStatsProvider interface {
	Stats() sql.DBStats
}

func New(database databaseStatsProvider) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "taskboard", Name: "http_requests_total", Help: "HTTP requests handled by method, route, and status class.",
		}, []string{"method", "route", "status_class"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "taskboard", Name: "http_request_duration_seconds", Help: "HTTP request latency by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		authAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "taskboard", Name: "auth_attempts_total", Help: "Authentication decisions by mechanism and outcome.",
		}, []string{"mechanism", "outcome"}),
		databasePing: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "taskboard", Name: "database_ping_total", Help: "Readiness database pings by outcome.",
		}, []string{"outcome"}),
		agentRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "taskboard", Name: "agent_run_operations_total", Help: "Agent run and lease operations by operation and outcome.",
		}, []string{"operation", "outcome"}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "taskboard", Name: "event_deliveries_total", Help: "In-process task event fan-out outcomes.",
		}, []string{"outcome"}),
		subscribers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "taskboard", Name: "event_subscribers", Help: "Current in-process task event subscribers.",
		}),
		push: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "taskboard", Name: "push_deliveries_total", Help: "Web Push delivery attempts by outcome.",
		}, []string{"outcome"}),
	}
	m.registry.MustRegister(m.httpRequests, m.httpDuration, m.authAttempts, m.databasePing, m.agentRuns, m.events, m.subscribers, m.push)
	if database != nil {
		m.registerDatabaseStats(database)
	}
	return m
}

func (m *Metrics) registerDatabaseStats(database databaseStatsProvider) {
	definitions := []struct {
		state string
		value func(sql.DBStats) float64
	}{
		{"max_open", func(stats sql.DBStats) float64 { return float64(stats.MaxOpenConnections) }},
		{"open", func(stats sql.DBStats) float64 { return float64(stats.OpenConnections) }},
		{"in_use", func(stats sql.DBStats) float64 { return float64(stats.InUse) }},
		{"idle", func(stats sql.DBStats) float64 { return float64(stats.Idle) }},
	}
	for _, definition := range definitions {
		definition := definition
		m.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "taskboard", Name: "database_connections", Help: "Database connection pool state.", ConstLabels: prometheus.Labels{"state": definition.state},
		}, func() float64 { return definition.value(database.Stats()) }))
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *Metrics) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := httpsnoop.CaptureMetrics(next, w, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		method := strings.ToUpper(r.Method)
		statusClass := strconv.Itoa(result.Code/100) + "xx"
		m.httpRequests.WithLabelValues(method, route, statusClass).Inc()
		m.httpDuration.WithLabelValues(method, route).Observe(result.Duration.Seconds())
	})
}

func (m *Metrics) ObserveAuth(mechanism, outcome string) {
	m.authAttempts.WithLabelValues(mechanism, outcome).Inc()
}

func (m *Metrics) ObserveDatabasePing(err error) {
	m.databasePing.WithLabelValues(outcome(err)).Inc()
}

func (m *Metrics) ObserveAgentRun(operation string, err error) {
	m.agentRuns.WithLabelValues(operation, outcome(err)).Inc()
}

func (m *Metrics) AddStaleRuns(count int64) {
	if count > 0 {
		m.agentRuns.WithLabelValues("sweep", "stale").Add(float64(count))
	}
}

func (m *Metrics) AddSubscriber()    { m.subscribers.Inc() }
func (m *Metrics) RemoveSubscriber() { m.subscribers.Dec() }
func (m *Metrics) ObserveEvent(outcome string) {
	m.events.WithLabelValues(outcome).Inc()
}
func (m *Metrics) ObservePush(outcome string) {
	m.push.WithLabelValues(outcome).Inc()
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
