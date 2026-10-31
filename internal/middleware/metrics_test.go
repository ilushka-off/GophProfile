package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func newMeteredRouter(m *HTTPMetrics) http.Handler {
	r := chi.NewRouter()
	r.Use(Tracing())
	r.Use(m.Middleware("/health"))
	r.Get("/health", func(http.ResponseWriter, *http.Request) {})
	r.Get("/api/v1/avatars/{avatar_id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	r.Post("/api/v1/avatars", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	})
	return r
}

func TestHTTPMetrics(t *testing.T) {
	recordSpans(t) // exemplar прикрепляется только внутри записываемого трейса
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(reg)
	router := newMeteredRouter(m)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/avatars/1", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/avatars/2", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/avatars", nil),
		httptest.NewRequest(http.MethodGet, "/wp-admin/../etc/passwd", nil),
		httptest.NewRequest("PROPFIND", "/api/v1/avatars", nil),
		httptest.NewRequest(http.MethodGet, "/health", nil),
	} {
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	want := `
# HELP http_requests_total HTTP requests by method, route template and status code.
# TYPE http_requests_total counter
http_requests_total{method="GET",route="/api/v1/avatars/{avatar_id}",status="404"} 2
http_requests_total{method="GET",route="unmatched",status="404"} 1
http_requests_total{method="OTHER",route="unmatched",status="405"} 1
http_requests_total{method="POST",route="/api/v1/avatars",status="200"} 1
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want), "http_requests_total"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(reg, "http_request_duration_seconds"); n != 4 {
		t.Errorf("duration series = %d, want 4", n)
	}
	if v := testutil.ToFloat64(m.inFlight); v != 0 {
		t.Errorf("in flight = %v after all requests finished", v)
	}

	// Наблюдения длительности несут exemplar с trace_id.
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if !hasExemplar(families, "http_request_duration_seconds", "trace_id") {
		t.Error("no trace_id exemplar on http_request_duration_seconds")
	}
}

func TestHTTPMetricsInFlight(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(reg)

	var during float64
	h := m.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		during = testutil.ToFloat64(m.inFlight)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if during != 1 {
		t.Errorf("in flight during request = %v, want 1", during)
	}
}

func hasExemplar(families []*dto.MetricFamily, name, label string) bool {
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, b := range m.GetHistogram().GetBucket() {
				for _, l := range b.GetExemplar().GetLabel() {
					if l.GetName() == label && l.GetValue() != "" {
						return true
					}
				}
			}
		}
	}
	return false
}
