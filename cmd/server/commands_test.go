package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthURL(t *testing.T) {
	tests := []struct {
		addr    string
		want    string
		wantErr bool
	}{
		{addr: ":8080", want: "http://127.0.0.1:8080/health"},
		{addr: "0.0.0.0:9000", want: "http://127.0.0.1:9000/health"},
		{addr: "[::]:8080", want: "http://127.0.0.1:8080/health"},
		{addr: "localhost:8080", want: "http://localhost:8080/health"},
		{addr: "8080", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := healthURL(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("healthURL(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("healthURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "healthy", status: http.StatusOK},
		{name: "degraded", status: http.StatusServiceUnavailable, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					t.Errorf("path = %q, want /health", r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			err := healthcheck(context.Background(), srv.Listener.Addr().String())
			if (err != nil) != tt.wantErr {
				t.Errorf("healthcheck() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestHealthcheckNoServer(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	if err := healthcheck(context.Background(), addr); err == nil {
		t.Error("healthcheck() error = nil, want error when nothing listens")
	}
}

func TestRunCommandUsage(t *testing.T) {
	for _, args := range [][]string{{"migrate"}, {"migrate", "down"}, {"serve"}, {"healthcheck", "extra"}} {
		if err := runCommand(args); !errors.Is(err, errUsage) {
			t.Errorf("runCommand(%q) error = %v, want errUsage", args, err)
		}
	}
}

func TestMigrateUpRequiresDatabaseURI(t *testing.T) {
	t.Setenv("DATABASE_URI", "")
	if err := migrateUp(); err == nil {
		t.Error("migrateUp() error = nil without DATABASE_URI")
	}
}
