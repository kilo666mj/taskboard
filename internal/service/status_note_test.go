package service

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestStatusNotesOverLimitAreRefusedNotClipped(t *testing.T) {
	service := testService(t, time.Minute)
	started, err := service.Start(t.Context(), model.StartRequest{Title: "Noisy task", Checklist: []string{"Step"}}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	fits := strings.Repeat("é", model.MaxStatusNoteRunes)
	task, err := service.Update(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: 1, CurrentNote: &fits}, "codex")
	if err != nil || task.CurrentNote != fits {
		t.Fatalf("note at the limit: err=%v note length=%d", err, utf8.RuneCountInString(task.CurrentNote))
	}
	long := fits + "x"
	for name, request := range map[string]model.UpdateRequest{
		"current_note": {ExpectedVersion: task.Version, CurrentNote: &long},
		"blocker":      {ExpectedVersion: task.Version, Status: model.TaskBlocked, Blocker: &long},
		"waiting_for":  {ExpectedVersion: task.Version, Status: model.TaskWaiting, WaitingFor: &long},
	} {
		_, err := service.Update(t.Context(), started.Task.ID, request, "codex")
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "task_message_add") {
			t.Fatalf("%s over the limit: err=%v, want validation error naming the field and task_message_add", name, err)
		}
	}
}

func TestClippedKeepsCharactersWhole(t *testing.T) {
	if got := clipped("  short  ", 10); got != "short" {
		t.Fatalf("clipped(short) = %q", got)
	}
	got := clipped(strings.Repeat("é", 10), 9)
	if !utf8.ValidString(got) || len(got) > 9 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped multibyte = %q (%d bytes)", got, len(got))
	}
}
