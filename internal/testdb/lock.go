// Package testdb provides a shared TestMain helper that serialises
// DB-using test packages against the single PostgreSQL instance they
// all read from. Each test package's main_test.go calls RunWithLock,
// which acquires a session-level advisory lock so that only one
// package's suite is touching the alertchain tables at a time and
// truncates the tables at the suite boundary.
//
// Production code does not use advisory locks; the lock key here is
// reserved for tests.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// lockKey is the advisory-lock key shared by every test package that
// touches alertchain's tables.
const lockKey = 0x416c6572 // 'Aler'

// truncatedTables is the full list of alertchain tables emptied at
// the start and end of each suite. Keep this list in sync with the
// schema migrations under migrations/.
var truncatedTables = []string{"mutes", "notifications", "alerts"}

// RunWithLock acquires the test-suite advisory lock, truncates the
// alertchain tables, runs the tests via m.Run, truncates again, and
// returns the exit code TestMain should pass to os.Exit.
//
// When DATABASE_URL is unset the lock and truncate steps are skipped;
// DB-backed tests skip themselves in that case.
func RunWithLock(m *testing.M) int {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return m.Run()
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdb: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: advisory lock: %v\n", err)
		return 1
	}
	if err := truncate(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: truncate (start): %v\n", err)
		return 1
	}

	code := m.Run()

	if err := truncate(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: truncate (end): %v\n", err)
	}
	// db.Close (deferred) releases the advisory lock by ending the
	// session that holds it.
	return code
}

func truncate(ctx context.Context, db *sql.DB) error {
	// Single statement so the operation is atomic from PostgreSQL's
	// perspective; if the table list grows, this still works.
	q := "TRUNCATE TABLE "
	for i, t := range truncatedTables {
		if i > 0 {
			q += ", "
		}
		q += t
	}
	_, err := db.ExecContext(ctx, q)
	return err
}
