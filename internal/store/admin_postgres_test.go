package store

import (
	"os"
	"strings"
	"testing"
)

func TestPostgresAdministrativeMutationAndAuditAreAtomic(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
	if baseURL == "" {
		t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
	}
	databaseURL, _, _ := postgresMigrationFixture(t, baseURL)
	database, err := OpenURL(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if _, _, err := database.CreateAgentCredential(t.Context(), "Build agent", "agent:build", nil, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateAgentCredential(t.Context(), "Deploy agent", "agent:deploy", nil, ""); err == nil {
		t.Fatal("credential creation without an audit actor succeeded")
	}
	credentials, err := database.ListAgentCredentials(t.Context())
	if err != nil || len(credentials) != 1 {
		t.Fatalf("credentials = %+v, %v", credentials, err)
	}
	audit, err := database.ListAdminAudit(t.Context(), 10)
	if err != nil || len(audit) != 1 || audit[0].Action != "credential.created" {
		t.Fatalf("audit = %+v, %v", audit, err)
	}
}
