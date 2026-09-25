package service

import (
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestFixedTaskTypeAppliesToNewWorkAndIgnoresTypeChanges(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	existing, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Before", Type: model.TaskPersonal}, human)
	if err != nil {
		t.Fatal(err)
	}

	tasks.SetFixedTaskType(model.TaskWork)
	if tasks.FixedTaskType() != model.TaskWork {
		t.Fatalf("fixed type = %q", tasks.FixedTaskType())
	}
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Captured", Type: model.TaskPersonal}, human)
	if err != nil || created.Type != model.TaskWork {
		t.Fatalf("created = %+v, %v", created, err)
	}
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Started", Type: model.TaskPersonal, Checklist: []string{"step"}}, AgentPrincipal("agent:worker"))
	if err != nil || started.Task.Type != model.TaskWork {
		t.Fatalf("started = %+v, %v", started.Task, err)
	}
	template, err := tasks.SaveTemplate(t.Context(), model.TemplateRequest{Name: "weekly", Title: "Weekly", Type: model.TaskPersonal})
	if err != nil || template.Type != model.TaskWork {
		t.Fatalf("template = %+v, %v", template, err)
	}

	personal := model.TaskPersonal
	updated, err := tasks.UpdateFor(t.Context(), existing.ID, model.UpdateRequest{ExpectedVersion: existing.Version, Type: &personal, Title: new("Renamed")}, human)
	if err != nil || updated.Type != model.TaskPersonal || updated.Title != "Renamed" {
		t.Fatalf("existing task after update = %+v, %v", updated, err)
	}
	work := model.TaskWork
	if updated, err = tasks.UpdateFor(t.Context(), existing.ID, model.UpdateRequest{ExpectedVersion: updated.Version, Type: &work}, human); err != nil || updated.Type != model.TaskPersonal {
		t.Fatalf("type change on fixed instance = %+v, %v", updated, err)
	}

	tasks.SetFixedTaskType("")
	if created, err = tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Chosen", Type: model.TaskPersonal}, human); err != nil || created.Type != model.TaskPersonal {
		t.Fatalf("per-task type = %+v, %v", created, err)
	}
}
