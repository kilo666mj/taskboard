// Command schema-migrate brings a database to this tree's schema and exits.
// The migration compatibility check builds it so the previous release's tests
// run against the current schema; see scripts/check-schema-compat.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/kilo666mj/taskboard/internal/store"
)

func main() {
	sqlitePath := flag.String("sqlite", "", "SQLite database path")
	postgresURL := flag.String("postgres", "", "PostgreSQL database URL")
	flag.Parse()
	if err := run(context.Background(), *sqlitePath, *postgresURL); err != nil {
		fmt.Fprintln(os.Stderr, "schema-migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, sqlitePath, postgresURL string) error {
	var database *store.Store
	var err error
	switch {
	case sqlitePath != "" && postgresURL == "":
		database, err = store.Open(ctx, sqlitePath)
	case postgresURL != "" && sqlitePath == "":
		database, err = store.OpenURL(ctx, postgresURL)
	default:
		return fmt.Errorf("pass exactly one of -sqlite or -postgres")
	}
	if err != nil {
		return err
	}
	return database.Close()
}
