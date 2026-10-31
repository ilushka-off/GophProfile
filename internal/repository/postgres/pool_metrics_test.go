package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Пул создаётся без подключения к базе, поэтому коллектор проверяется
// без контейнера: статистика пустого пула известна заранее.
func TestPoolCollector(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/db?pool_max_conns=7")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	c := NewPoolCollector(pool)
	if n := testutil.CollectAndCount(c); n != 13 {
		t.Errorf("collected %d series, want 13", n)
	}

	want := `
# HELP pgxpool_max_conns Maximum size of the pool.
# TYPE pgxpool_max_conns gauge
pgxpool_max_conns 7
# HELP pgxpool_acquired_conns Connections currently in use.
# TYPE pgxpool_acquired_conns gauge
pgxpool_acquired_conns 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "pgxpool_max_conns", "pgxpool_acquired_conns"); err != nil {
		t.Error(err)
	}
}
