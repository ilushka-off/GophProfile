package postgres

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// storageQueryTimeout ограничивает запрос объёма хранилища при опросе /metrics:
// медленная база не должна подвешивать сбор остальных метрик.
const storageQueryTimeout = 2 * time.Second

// StorageCollector считает объём оригиналов неудалённых аватарок прямо
// в БД при каждом опросе: значение не теряется при рестарте и одинаково
// у всех экземпляров server.
//
//   - avatars_storage_bytes{user_id} — объём по пользователю (из ТЗ);
//   - avatars_storage_bytes_total — общий объём, чтобы дашборду не нужно было
//     суммировать серии всех пользователей.
//
// Если база недоступна, серии на этом опросе пропускаются, а не роняют весь
// /metrics: недоступность БД видна в /health и метриках пула.
type StorageCollector struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	perUser *prometheus.Desc
	total   *prometheus.Desc
}

// NewStorageCollector создаёт коллектор; зарегистрировать его нужно самому.
func NewStorageCollector(pool *pgxpool.Pool, logger *slog.Logger) *StorageCollector {
	return &StorageCollector{
		pool:   pool,
		logger: logger,
		perUser: prometheus.NewDesc("avatars_storage_bytes",
			"Size of original files of a user's avatars that are not deleted, in bytes.",
			[]string{"user_id"}, nil),
		total: prometheus.NewDesc("avatars_storage_bytes_total",
			"Size of original files of all avatars that are not deleted, in bytes.",
			nil, nil),
	}
}

func (c *StorageCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.perUser
	ch <- c.total
}

func (c *StorageCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), storageQueryTimeout)
	defer cancel()

	usage, err := c.usageByUser(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "collect storage metrics", "err", err)
		return
	}

	var total int64
	for userID, bytes := range usage {
		total += bytes
		ch <- prometheus.MustNewConstMetric(c.perUser, prometheus.GaugeValue, float64(bytes), userID)
	}
	ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, float64(total))
}

func (c *StorageCollector) usageByUser(ctx context.Context) (map[string]int64, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT user_id, SUM(size_bytes)::bigint
		FROM avatars
		WHERE deleted_at IS NULL
		GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	usage := make(map[string]int64)
	for rows.Next() {
		var userID string
		var bytes int64
		if err := rows.Scan(&userID, &bytes); err != nil {
			return nil, err
		}
		usage[userID] = bytes
	}
	return usage, rows.Err()
}
