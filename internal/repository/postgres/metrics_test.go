package postgres

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func (s *RepositorySuite) TestStorageCollector() {
	ctx := context.Background()
	s.createAvatar("alice")
	s.createAvatar("alice")
	s.createAvatar("bob")
	deleted := s.createAvatar("bob")
	s.Require().NoError(s.repo.SoftDelete(ctx, deleted.ID))

	c := NewStorageCollector(s.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	want := `
# HELP avatars_storage_bytes Size of original files of a user's avatars that are not deleted, in bytes.
# TYPE avatars_storage_bytes gauge
avatars_storage_bytes{user_id="alice"} 2.048e+06
avatars_storage_bytes{user_id="bob"} 1.024e+06
# HELP avatars_storage_bytes_all Size of original files of all avatars that are not deleted, in bytes.
# TYPE avatars_storage_bytes_all gauge
avatars_storage_bytes_all 3.072e+06
`
	s.NoError(testutil.CollectAndCompare(c, strings.NewReader(want)))
}

func (s *RepositorySuite) TestStorageCollectorEmpty() {
	c := NewStorageCollector(s.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Equal(1, testutil.CollectAndCount(c), "only the total series without avatars")
}
