package api

// setup_test.go houses TestMain for this package and delegates the
// DB-suite lifecycle (advisory lock + TRUNCATE) to internal/testdb,
// so this package and internal/ui share one canonical setup.

import (
	"os"
	"testing"

	"github.com/zinrai/alertchain/internal/testdb"
)

func TestMain(m *testing.M) {
	os.Exit(testdb.RunWithLock(m))
}
