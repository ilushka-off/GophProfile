package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"go-avatar-service/internal/broker"
)

func TestInstrument(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	boom := errors.New("broken image")

	results := map[string]error{"ok-1": nil, "ok-2": nil, "bad": boom}
	h := m.Instrument(func(_ context.Context, msg broker.Message) error {
		return results[msg.ID]
	})

	published := time.Now().Add(-2 * time.Second)
	for _, id := range []string{"ok-1", "ok-2", "bad"} {
		err := h(context.Background(), broker.Message{ID: id, Type: broker.TypeAvatarUploaded, PublishedAt: published})
		if !errors.Is(err, results[id]) {
			t.Errorf("%s: err = %v, want it passed through", id, err)
		}
	}
	// Без времени публикации задержка не пишется.
	_ = h(context.Background(), broker.Message{ID: "ok-1", Type: broker.TypeAvatarDeleted})

	want := `
# HELP avatars_processing_total Processed broker events by type and outcome: success or error (sent to the DLQ).
# TYPE avatars_processing_total counter
avatars_processing_total{status="error",type="avatar.uploaded"} 1
avatars_processing_total{status="success",type="avatar.deleted"} 1
avatars_processing_total{status="success",type="avatar.uploaded"} 2
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want), "avatars_processing_total"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(reg, "avatars_processing_duration_seconds"); n != 2 {
		t.Errorf("duration series = %d, want 2", n)
	}

	lag := gatherHistogram(t, reg, "avatars_processing_lag_seconds")
	if lag.count != 3 {
		t.Errorf("lag observations = %d, want 3 (only messages with a publish time)", lag.count)
	}
	if avg := lag.sum / float64(lag.count); avg < 2 || avg > 10 {
		t.Errorf("average lag = %.2fs, want about 2s", avg)
	}
}

func TestInstrumentSkipsShutdown(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h := m.Instrument(func(ctx context.Context, _ broker.Message) error { return ctx.Err() })
	_ = h(ctx, broker.Message{Type: broker.TypeAvatarUploaded})

	if n := testutil.CollectAndCount(reg, "avatars_processing_total"); n != 0 {
		t.Errorf("interrupted processing is counted: %d series", n)
	}
}

type histogram struct {
	count uint64
	sum   float64
}

func gatherHistogram(t *testing.T, reg *prometheus.Registry, name string) histogram {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var h histogram
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			h.count += metric.GetHistogram().GetSampleCount()
			h.sum += metric.GetHistogram().GetSampleSum()
		}
	}
	return h
}
