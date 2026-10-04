package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvironmentFileUnquotesTemplateValues(t *testing.T) {
	values, err := parseEnvironmentFile(strings.NewReader(`# comment
TASKBOARD_LEASE_SECONDS=900

TASKBOARD_AGENT_POLICIES_JSON="{\"agent:worker\":{\"capabilities\":[\"task:read\"]}}"
TASKBOARD_TASK_TYPE=''
`))
	if err != nil {
		t.Fatal(err)
	}
	if values["TASKBOARD_LEASE_SECONDS"] != "900" || values["TASKBOARD_TASK_TYPE"] != "" {
		t.Fatalf("values = %v", values)
	}
	if got := values["TASKBOARD_AGENT_POLICIES_JSON"]; got != `{"agent:worker":{"capabilities":["task:read"]}}` {
		t.Fatalf("policies = %q", got)
	}
	if _, err := parseEnvironmentFile(strings.NewReader("not an assignment\n")); err == nil {
		t.Fatal("malformed line was accepted")
	}
}

func TestCheckConfigRejectsUnknownCapabilityFromEnvironmentFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, policies string) string {
		path := filepath.Join(dir, name)
		content := "TASKBOARD_ALLOW_INSECURE=true\nTASKBOARD_DATABASE_PATH=" + filepath.Join(dir, "unused.db") + "\nTASKBOARD_AGENT_POLICIES_JSON=" + policies + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("TASKBOARD_ALLOW_INSECURE", "")
	t.Setenv("TASKBOARD_DATABASE_PATH", "")
	t.Setenv("TASKBOARD_AGENT_POLICIES_JSON", "")
	if err := checkConfig(write("good.env", `"{\"agent:worker\":{\"capabilities\":[\"task:read\",\"task:skip\"]}}"`)); err != nil {
		t.Fatalf("valid environment rejected: %v", err)
	}
	err := checkConfig(write("bad.env", `"{\"agent:worker\":{\"capabilities\":[\"task:teleport\"]}}"`))
	if err == nil || !strings.Contains(err.Error(), "task:teleport") {
		t.Fatalf("unknown capability error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "unused.db")); !os.IsNotExist(err) {
		t.Fatalf("check-config touched the database: %v", err)
	}
}
