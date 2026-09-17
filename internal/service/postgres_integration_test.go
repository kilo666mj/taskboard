package service

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestPostgresTaskLifecycleAndAtomicClaim(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
	if databaseURL == "" {
		t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
	}

	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "taskboard_test_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	var databases [2]*store.Store
	var openErrors [2]error
	var openWait sync.WaitGroup
	for index := range databases {
		openWait.Add(1)
		go func(index int) {
			defer openWait.Done()
			databases[index], openErrors[index] = store.OpenURL(t.Context(), parsed.String())
		}(index)
	}
	openWait.Wait()
	if openErrors[0] != nil || openErrors[1] != nil {
		for _, database := range databases {
			if database != nil {
				_ = database.Close()
			}
		}
		_, _ = admin.ExecContext(t.Context(), `DROP SCHEMA `+schema+` CASCADE`)
		_ = admin.Close()
		t.Fatalf("concurrent PostgreSQL migrations: %v, %v", openErrors[0], openErrors[1])
	}
	database := databases[0]
	t.Cleanup(func() {
		for _, database := range databases {
			if err := database.Close(); err != nil {
				t.Errorf("close PostgreSQL store: %v", err)
			}
		}
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})

	tasks := New(database, time.Minute)
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{
		Title:      "PostgreSQL pickup",
		Visibility: model.VisibilityAgent,
		Checklist:  []string{"claim exactly once"},
	}, HumanPrincipal("creator"))
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		claim model.StartResult
		agent string
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, agent := range []string{"agent-one", "agent-two"} {
		wait.Add(1)
		go func(agent string) {
			defer wait.Done()
			<-start
			claim, err := tasks.ClaimFor(t.Context(), created.ID, model.ClaimRequest{ExpectedVersion: created.Version}, AgentPrincipal(agent))
			results <- result{claim: claim, agent: agent, err: err}
		}(agent)
	}
	close(start)
	wait.Wait()
	close(results)

	successes, rejected := 0, 0
	var winner result
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result
			if result.claim.Task.Owner == "" || result.claim.Task.Status != model.TaskActive {
				t.Fatalf("successful claim = %+v", result.claim)
			}
		case errors.Is(result.err, ErrConflict), errors.Is(result.err, ErrValidation):
			rejected++
		default:
			t.Fatalf("unexpected claim error: %v", result.err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("claim outcomes = %d success, %d rejected", successes, rejected)
	}
	note := "PostgreSQL claim completed"
	completed, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{
		ExpectedVersion: winner.claim.Task.Version,
		RunID:           winner.claim.Run.ID,
		Status:          model.TaskDone,
		CompleteItemIDs: []string{winner.claim.Task.Items[0].ID},
		CurrentNote:     &note,
	}, AgentPrincipal(winner.agent))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.TaskDone || completed.CompletedAt == nil {
		t.Fatalf("completed PostgreSQL task = %+v", completed)
	}

	identity := store.BrowserIdentity{Subject: "postgres-user", Groups: []string{"taskboard-users"}}
	token, _, err := database.CreateBrowserSession(t.Context(), identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	loaded, valid, err := database.BrowserSession(t.Context(), token)
	if err != nil || !valid || loaded.Subject != identity.Subject {
		t.Fatalf("PostgreSQL browser session = %+v, valid=%v, err=%v", loaded, valid, err)
	}

	if _, err := tasks.SaveTemplate(t.Context(), model.TemplateRequest{Name: "postgres-template", Title: "Template", Checklist: []string{"step"}}); err != nil {
		t.Fatal(err)
	}
	templates, err := tasks.ListTemplates(t.Context())
	if err != nil || len(templates) != 1 || templates[0].Name != "postgres-template" {
		t.Fatalf("PostgreSQL templates = %+v, err=%v", templates, err)
	}

	subscription := store.PushSubscription{Endpoint: "https://push.example/1", P256DH: "key", Auth: "auth", OwnerID: identity.Subject, NotifyProgress: true, PreferencesSet: true}
	if err := database.SavePushSubscription(t.Context(), subscription); err != nil {
		t.Fatal(err)
	}
	conflicting := subscription
	conflicting.OwnerID = "other-postgres-user"
	conflicting.P256DH = "replacement-key"
	if err := database.SavePushSubscription(t.Context(), conflicting); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflicting PostgreSQL subscription error = %v, want conflict", err)
	}
	if err := database.UpdatePushPreferences(t.Context(), subscription.Endpoint, identity.Subject, false, true, true); err != nil {
		t.Fatal(err)
	}
	subscriptions, err := database.ListPushSubscriptions(t.Context())
	if err != nil || len(subscriptions) != 1 || subscriptions[0].OwnerID != identity.Subject || subscriptions[0].P256DH != "key" || subscriptions[0].NotifyProgress || !subscriptions[0].NotifyReminders || !subscriptions[0].NotifySummaries {
		t.Fatalf("PostgreSQL subscriptions = %+v, err=%v", subscriptions, err)
	}
}
