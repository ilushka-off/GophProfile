package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolCollector отдаёт статистику пула соединений pgx: сколько соединений
// занято и свободно, сколько раз и как долго ждали свободного. Значения
// читаются из pool.Stat() при каждом опросе.
type PoolCollector struct {
	pool *pgxpool.Pool

	acquired, idle, total, max, constructing  *prometheus.Desc
	acquires, acquireSeconds                  *prometheus.Desc
	emptyAcquires, emptyAcquireSeconds        *prometheus.Desc
	canceledAcquires, newConns, destroyedConn *prometheus.Desc
}

// NewPoolCollector создаёт коллектор; зарегистрировать его нужно самому.
func NewPoolCollector(pool *pgxpool.Pool) *PoolCollector {
	desc := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc("pgxpool_"+name, help, labels, nil)
	}
	return &PoolCollector{
		pool:         pool,
		acquired:     desc("acquired_conns", "Connections currently in use."),
		idle:         desc("idle_conns", "Idle connections in the pool."),
		total:        desc("total_conns", "All connections in the pool: acquired, idle and being constructed."),
		max:          desc("max_conns", "Maximum size of the pool."),
		constructing: desc("constructing_conns", "Connections being established right now."),
		acquires:     desc("acquire_count_total", "Successful connection acquires."),
		acquireSeconds: desc("acquire_duration_seconds_total",
			"Total time spent acquiring connections, in seconds."),
		emptyAcquires: desc("empty_acquire_count_total",
			"Acquires that had to wait because the pool had no idle connection."),
		emptyAcquireSeconds: desc("empty_acquire_wait_seconds_total",
			"Total time spent waiting for a connection when the pool was empty, in seconds."),
		canceledAcquires: desc("canceled_acquire_count_total", "Acquires canceled by their context."),
		newConns:         desc("new_conns_total", "Connections opened by the pool."),
		destroyedConn: desc("destroyed_conns_total",
			"Connections closed by the pool because of max lifetime or max idle time.", "reason"),
	}
}

func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.acquired, c.idle, c.total, c.max, c.constructing,
		c.acquires, c.acquireSeconds, c.emptyAcquires, c.emptyAcquireSeconds,
		c.canceledAcquires, c.newConns, c.destroyedConn,
	} {
		ch <- d
	}
}

func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	gauge := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	counter := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, labels...)
	}

	gauge(c.acquired, float64(s.AcquiredConns()))
	gauge(c.idle, float64(s.IdleConns()))
	gauge(c.total, float64(s.TotalConns()))
	gauge(c.max, float64(s.MaxConns()))
	gauge(c.constructing, float64(s.ConstructingConns()))
	counter(c.acquires, float64(s.AcquireCount()))
	counter(c.acquireSeconds, s.AcquireDuration().Seconds())
	counter(c.emptyAcquires, float64(s.EmptyAcquireCount()))
	counter(c.emptyAcquireSeconds, s.EmptyAcquireWaitTime().Seconds())
	counter(c.canceledAcquires, float64(s.CanceledAcquireCount()))
	counter(c.newConns, float64(s.NewConnsCount()))
	counter(c.destroyedConn, float64(s.MaxLifetimeDestroyCount()), "max_lifetime")
	counter(c.destroyedConn, float64(s.MaxIdleDestroyCount()), "max_idle")
}
