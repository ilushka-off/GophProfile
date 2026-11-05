package observability

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	MetricsHandler(NewRegistry()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_gc_duration_seconds"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/metrics has no %s", want)
		}
	}
}

func TestMetricsHandlerOpenMetrics(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text; version=1.0.0")
	rec := httptest.NewRecorder()
	MetricsHandler(NewRegistry()).ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Errorf("Content-Type = %q, want OpenMetrics for exemplars", ct)
	}
}

func TestServeMetrics(t *testing.T) {
	// Свободный порт: занимаем и сразу освобождаем.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	livez := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "alive") })
	go func() { done <- ServeMetrics(ctx, addr, NewRegistry(), map[string]http.Handler{"/livez": livez}) }()

	var body string
	for range 50 {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Fatalf("metrics server did not answer: %q", body)
	}

	resp, err := http.Get("http://" + addr + "/livez")
	if err != nil {
		t.Fatalf("GET /livez: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != "alive" {
		t.Errorf("GET /livez = %d %q, want 200 \"alive\"", resp.StatusCode, b)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeMetrics() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeMetrics did not stop after ctx cancel")
	}
}

func TestServeMetricsBusyPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := ServeMetrics(context.Background(), l.Addr().String(), NewRegistry(), nil); err == nil {
		t.Error("ServeMetrics() error = nil on a busy port")
	}
}
