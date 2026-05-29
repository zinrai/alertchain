package ui

// main_test.go owns the DB lifecycle for this package's tests:
// acquire a PostgreSQL advisory lock so test packages that share the
// same database (currently this one and internal/api) do not race for
// the same tables, truncate once at the suite boundary, and run the
// tests. Individual tests do not truncate.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// dbTestLockKey is the advisory-lock key shared by every test package
// that touches alertchain's tables. It guarantees suite-level mutual
// exclusion against the same database; production code does not use
// advisory locks, so this key is reserved for test harnesses.
const dbTestLockKey = 0x416c6572 // 'Aler'

func TestMain(m *testing.M) {
	os.Exit(runWithDBLock(m))
}

func runWithDBLock(m *testing.M) int {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return m.Run()
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, dbTestLockKey); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: advisory lock: %v\n", err)
		return 1
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE mutes, notifications`); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: truncate (start): %v\n", err)
		return 1
	}

	code := m.Run()

	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE mutes, notifications`); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: truncate (end): %v\n", err)
	}
	return code
}
