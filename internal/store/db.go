package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// DB keeps SQL portable without spreading driver-specific placeholders through
// the service layer. Taskboard's queries use question-mark placeholders and are
// rebound to PostgreSQL's positional placeholders at the database boundary.
type DB struct {
	raw     *sql.DB
	dialect Dialect
}

func (d *DB) BeginTx(ctx context.Context, options *sql.TxOptions) (*Tx, error) {
	tx, err := d.raw.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: tx, dialect: d.dialect}, nil
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.raw.ExecContext(ctx, rebind(d.dialect, query), args...)
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.raw.QueryContext(ctx, rebind(d.dialect, query), args...)
}

func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.raw.QueryRowContext(ctx, rebind(d.dialect, query), args...)
}

func (d *DB) Dialect() Dialect   { return d.dialect }
func (d *DB) Stats() sql.DBStats { return d.raw.Stats() }

type Tx struct {
	raw     *sql.Tx
	dialect Dialect
}

func (t *Tx) Commit() error   { return t.raw.Commit() }
func (t *Tx) Rollback() error { return t.raw.Rollback() }

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, rebind(t.dialect, query), args...)
}

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.raw.QueryContext(ctx, rebind(t.dialect, query), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, rebind(t.dialect, query), args...)
}

func rebind(dialect Dialect, query string) string {
	if dialect != DialectPostgres || !strings.Contains(query, "?") {
		return query
	}
	var builder strings.Builder
	builder.Grow(len(query) + 8)
	parameter := 1
	inSingleQuote := false
	for index := 0; index < len(query); index++ {
		character := query[index]
		if character == '\'' {
			builder.WriteByte(character)
			if inSingleQuote && index+1 < len(query) && query[index+1] == '\'' {
				builder.WriteByte(query[index+1])
				index++
				continue
			}
			inSingleQuote = !inSingleQuote
			continue
		}
		if character == '?' && !inSingleQuote {
			_, _ = fmt.Fprintf(&builder, "$%d", parameter)
			parameter++
			continue
		}
		builder.WriteByte(character)
	}
	return builder.String()
}
