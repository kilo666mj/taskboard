package service

import (
	"errors"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkerMatchingIsSeparateFromAuthorization(t *testing.T) {
	tasks := testService(t, time.Minute)
	creator := HumanPrincipal("human:owner")
	reviewer := HumanPrincipal("human:reviewer")
	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Kubernetes work", Visibility: model.VisibilityAgent}, creator)
	if err != nil {
		t.Fatal(err)
	}
	task, err = tasks.SetTaskRequirementsFor(t.Context(), task.ID, model.SetTaskRequirementsRequest{Requirements: []string{"repo:org/app", "kubernetes"}, ExpectedVersion: task.Version}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if task.LastEditedBy != reviewer.ID {
		t.Fatalf("requirements editor = %q, want %q", task.LastEditedBy, reviewer.ID)
	}
	worker := AgentPrincipal("agent:worker")
	items, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Statuses: []model.TaskStatus{model.TaskQueued}, Limit: 10}, worker)
	if err != nil || len(items.Tasks) != 0 {
		t.Fatalf("unadvertised items = %+v, %v", items, err)
	}
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"repo:org/app"}, Capacity: 2, TTLSeconds: 60}, worker); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version, IdempotencyKey: "missing-match"}, worker); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing match claim = %v", err)
	}
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"repo:org/app", "kubernetes", "task:claim"}, Capacity: 2, TTLSeconds: 60}, worker); err != nil {
		t.Fatal(err)
	}
	items, err = tasks.ListFor(t.Context(), model.ListTasksRequest{Statuses: []model.TaskStatus{model.TaskQueued}, Limit: 10}, worker)
	if err != nil || len(items.Tasks) != 1 {
		t.Fatalf("matched items = %+v, %v", items, err)
	}
	policy := DefaultAgentPolicy()
	delete(policy.Capabilities, CapabilityTaskClaim)
	unauthorized := AgentPrincipalWithPolicy("agent:no-claim", policy)
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"repo:org/app", "kubernetes"}, Capacity: 2, TTLSeconds: 60}, unauthorized); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version, IdempotencyKey: "no-claim-auth"}, unauthorized); !errors.Is(err, ErrForbidden) {
		t.Fatalf("security capability error = %v", err)
	}
	if _, err = tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version, IdempotencyKey: "matched-claim"}, worker); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRequirementsRouteAgentLaneTasks(t *testing.T) {
	tasks := testService(t, time.Minute)
	if err := tasks.SetDefaultRequirements([]string{"runner:k8s-job"}); err != nil {
		t.Fatal(err)
	}
	if err := tasks.SetDefaultRequirements([]string{"Not A Token!"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid default error = %v", err)
	}
	person := HumanPrincipal("human:owner")
	pickup, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Pickup", Visibility: model.VisibilityAgent}, person)
	if err != nil || !slices.Equal(pickup.Requirements, []string{"runner:k8s-job"}) {
		t.Fatalf("defaulted pickup = %+v, %v", pickup.Requirements, err)
	}
	explicit, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Local", Visibility: model.VisibilityAgent, Requirements: []string{"runner:local"}}, person)
	if err != nil || !slices.Equal(explicit.Requirements, []string{"runner:local"}) {
		t.Fatalf("explicit requirements = %+v, %v", explicit.Requirements, err)
	}
	private, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Mine"}, person)
	if err != nil || len(private.Requirements) != 0 {
		t.Fatalf("private task requirements = %+v, %v", private.Requirements, err)
	}
	agentLane := model.VisibilityAgent
	moved, err := tasks.UpdateFor(t.Context(), private.ID, model.UpdateRequest{ExpectedVersion: private.Version, Visibility: &agentLane}, person)
	if err != nil || !slices.Equal(moved.Requirements, []string{"runner:k8s-job"}) {
		t.Fatalf("moved task requirements = %+v, %v", moved.Requirements, err)
	}

	local := AgentPrincipal("agent:local-dispatcher")
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"runner:local"}, Capacity: 1, TTLSeconds: 60}, local); err != nil {
		t.Fatal(err)
	}
	listed, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Statuses: []model.TaskStatus{model.TaskQueued}, Visibility: model.VisibilityAgent, Limit: 10}, local)
	if err != nil || len(listed.Tasks) != 1 || listed.Tasks[0].ID != explicit.ID {
		t.Fatalf("local dispatcher pickup = %+v, %v", listed.Tasks, err)
	}
}

func TestRecurringOccurrenceKeepsRequirements(t *testing.T) {
	tasks := testService(t, time.Minute)
	person := HumanPrincipal("human:owner")
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Weekly sweep", Visibility: model.VisibilityAgent, DueDate: "2026-09-16", Recurrence: "weekly", Requirements: []string{"runner:local", "repo:org/app"}}, person)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.Update(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version, Status: model.TaskDone}, person.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := tasks.List(t.Context(), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range listed {
		if task.ID != created.ID && !slices.Equal(task.Requirements, []string{"repo:org/app", "runner:local"}) {
			t.Fatalf("next occurrence requirements = %+v", task.Requirements)
		}
	}
}

func TestAgentsSetOnlyPolicyAllowedRequirements(t *testing.T) {
	tasks := testService(t, time.Minute)
	if err := tasks.SetDefaultRequirements([]string{"runner:k8s-job"}); err != nil {
		t.Fatal(err)
	}
	unprivileged := AgentPrincipal("agent:bridge")
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Routed", Requirements: []string{"runner:local"}}, unprivileged); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unallowed requirement error = %v", err)
	}
	defaulted, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Default"}, unprivileged)
	if err != nil || !slices.Equal(defaulted.Requirements, []string{"runner:k8s-job"}) {
		t.Fatalf("agent default requirements = %+v, %v", defaulted.Requirements, err)
	}
	policy := DefaultAgentPolicy()
	policy.AllowedRequirements = map[string]bool{"runner:local": true}
	bridge := AgentPrincipalWithPolicy("agent:bridge", policy)
	routed, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Routed", Requirements: []string{"Runner:Local"}}, bridge)
	if err != nil || !slices.Equal(routed.Requirements, []string{"runner:local"}) {
		t.Fatalf("allowed requirements = %+v, %v", routed.Requirements, err)
	}
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Mixed", Requirements: []string{"runner:local", "aws"}}, bridge); !errors.Is(err, ErrForbidden) {
		t.Fatalf("partly allowed requirement error = %v", err)
	}
}

func TestWorkerCapabilitiesListLiveAdvertisementsOnly(t *testing.T) {
	tasks := testService(t, time.Minute)
	now := time.Now().UTC()
	for _, item := range []model.WorkerAdvertisement{
		// An advertiser that predates descriptions, the most recent one.
		{Principal: "agent:mod", Capabilities: []string{"discussion", "profile:pickup"}, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)},
		{Principal: "agent:new", Capabilities: []string{"profile:pickup", "runner:k8s-job"}, Descriptions: map[string]string{"profile:pickup": "New text"}, UpdatedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute)},
		{Principal: "agent:old", Capabilities: []string{"profile:pickup", "runner:k8s-job"}, Descriptions: map[string]string{"profile:pickup": "Old text", "runner:k8s-job": "Runs in a Job"}, UpdatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Minute)},
		{Principal: "agent:gone", Capabilities: []string{"profile:retired"}, Descriptions: map[string]string{"profile:retired": "Gone"}, UpdatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)},
	} {
		if err := store.UpsertWorkerAdvertisement(t.Context(), tasks.store.DB(), item); err != nil {
			t.Fatal(err)
		}
	}
	items, err := tasks.WorkerCapabilitiesFor(t.Context(), HumanPrincipal("human:owner"))
	want := []model.WorkerCapability{{Capability: "discussion"}, {Capability: "profile:pickup", Description: "New text"}, {Capability: "runner:k8s-job", Description: "Runs in a Job"}}
	if err != nil || !slices.Equal(items, want) {
		t.Fatalf("capabilities = %+v, %v", items, err)
	}
	if _, err := tasks.WorkerCapabilitiesFor(t.Context(), AgentPrincipal("agent:one")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent capabilities error = %v", err)
	}
}

func TestWorkerAdvertisementDescriptionsAreOptionalAndBounded(t *testing.T) {
	tasks := testService(t, time.Minute)
	worker := AgentPrincipal("agent:worker")
	if _, err := tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"runner:k8s-job"}, TTLSeconds: 60}, worker); err != nil {
		t.Fatalf("advertisement without descriptions = %v", err)
	}
	item, err := tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"runner:k8s-job", "profile:pickup"}, Descriptions: map[string]string{" Runner:K8s-Job ": "Runs in a\n  staging Job", "profile:pickup": " "}, TTLSeconds: 60}, worker)
	if err != nil || !maps.Equal(item.Descriptions, map[string]string{"runner:k8s-job": "Runs in a staging Job"}) {
		t.Fatalf("normalized descriptions = %+v, %v", item.Descriptions, err)
	}
	stored, err := tasks.store.GetWorkerAdvertisement(t.Context(), worker.ID)
	if err != nil || !maps.Equal(stored.Descriptions, item.Descriptions) {
		t.Fatalf("stored descriptions = %+v, %v", stored.Descriptions, err)
	}
	for name, descriptions := range map[string]map[string]string{
		"unadvertised": {"profile:other": "Not advertised"},
		"too long":     {"runner:k8s-job": strings.Repeat("x", 301)},
	} {
		if _, err := tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"runner:k8s-job"}, Descriptions: descriptions, TTLSeconds: 60}, worker); !errors.Is(err, ErrValidation) {
			t.Fatalf("%s description error = %v", name, err)
		}
	}
}
