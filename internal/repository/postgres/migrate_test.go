package postgres

import (
	"strings"
	"testing"
)

func TestToPgxMigrateURI(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "postgres scheme", in: "postgres://u:p@localhost:5432/db", want: "pgx5://u:p@localhost:5432/db"},
		{name: "postgresql scheme", in: "postgresql://u:p@localhost:5432/db", want: "pgx5://u:p@localhost:5432/db"},
		{name: "upper case scheme", in: "POSTGRES://u:p@localhost:5432/db", want: "pgx5://u:p@localhost:5432/db"},
		{name: "query params kept", in: "postgres://u:p@localhost:5432/db?sslmode=disable", want: "pgx5://u:p@localhost:5432/db?sslmode=disable"},
		{name: "unsupported scheme", in: "mysql://u:secret@localhost:3306/db", wantErr: true},
		{name: "empty string", in: "", wantErr: true},
		{name: "invalid uri", in: "postgres://u:secret@local host:5432/db", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toPgxMigrateURI(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Errorf("error leaks password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
