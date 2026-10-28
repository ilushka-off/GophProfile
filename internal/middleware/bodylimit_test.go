package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// countingReader считает, сколько байт тела прочитали.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

func TestMaxBodySize(t *testing.T) {
	tooLarge := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}

	tests := []struct {
		name          string
		body          string
		contentLength int64 // -1 — chunked, длина неизвестна
		wantStatus    int
		wantCalled    bool
		wantDrained   bool // тело дочитано до конца
		wantClose     bool // ответ с Connection: close
	}{
		{"within limit", "12345", 5, http.StatusOK, true, true, false},
		{"declared too large is drained and rejected", "123456", 6, http.StatusRequestEntityTooLarge, false, true, false},
		{"far too large is not read", strings.Repeat("x", 21), 21, http.StatusRequestEntityTooLarge, false, false, true},
		{"chunked over limit fails on read", "123456789", -1, http.StatusRequestEntityTooLarge, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := MaxBodySize(5, tooLarge)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if _, err := io.ReadAll(r.Body); err != nil {
					var maxErr *http.MaxBytesError
					if !errors.As(err, &maxErr) {
						t.Errorf("read error = %v, want *http.MaxBytesError", err)
					}
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))

			body := &countingReader{r: strings.NewReader(tt.body)}
			req := httptest.NewRequest(http.MethodPost, "/", body)
			req.ContentLength = tt.contentLength
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if called != tt.wantCalled {
				t.Errorf("handler called = %v, want %v", called, tt.wantCalled)
			}
			if drained := body.read == len(tt.body); drained != tt.wantDrained {
				t.Errorf("read %d of %d bytes, want drained = %v", body.read, len(tt.body), tt.wantDrained)
			}
			if got := rec.Header().Get("Connection") == "close"; got != tt.wantClose {
				t.Errorf("Connection: close = %v, want %v", got, tt.wantClose)
			}
		})
	}
}

// Через настоящий сервер клиент получает 413, а не обрыв соединения.
func TestMaxBodySizeOverNetwork(t *testing.T) {
	const limit = 1 << 20
	srv := httptest.NewServer(MaxBodySize(limit, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	defer srv.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	for _, size := range []int{limit + 1, 2 * limit} {
		resp, err := client.Post(srv.URL, "application/octet-stream", bytes.NewReader(make([]byte, size)))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("size %d: status = %d, want 413", size, resp.StatusCode)
		}
	}
}
