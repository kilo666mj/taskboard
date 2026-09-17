package store

import "testing"

func TestRebindPostgres(t *testing.T) {
	query := "SELECT '?' AS literal, id FROM tasks WHERE visibility=? AND owner=?"
	got := rebind(DialectPostgres, query)
	want := "SELECT '?' AS literal, id FROM tasks WHERE visibility=$1 AND owner=$2"
	if got != want {
		t.Fatalf("rebound query = %q, want %q", got, want)
	}
	if got := rebind(DialectSQLite, query); got != query {
		t.Fatalf("SQLite query changed to %q", got)
	}
}

func TestOpenURLRejectsNonPostgres(t *testing.T) {
	if _, err := OpenURL(t.Context(), "sqlite:///tmp/taskboard.db"); err == nil {
		t.Fatal("SQLite URL was accepted as PostgreSQL")
	}
}
