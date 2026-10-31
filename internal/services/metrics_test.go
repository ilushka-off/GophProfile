package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestServiceMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := newDeps()
	svc, err := NewAvatarService(d.repo, d.storage, d.publisher, testMaxUploadSize, WithMetrics(NewMetrics(reg)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := svc.Upload(ctx, uploadInput(encodePNG(t, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Upload(ctx, uploadInput([]byte("not an image"))); err == nil {
		t.Fatal("invalid file is accepted")
	}
	d.publisher.err = errors.New("broker is down")
	if _, err := svc.Upload(ctx, uploadInput(encodePNG(t, 8, 8))); err == nil {
		t.Fatal("upload succeeded with the broker down")
	}
	d.publisher.err = nil

	d.repo.byID = ownedAvatar("user-42")
	if err := svc.Delete(ctx, "user-42", testAvatarID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "intruder", testAvatarID); err == nil {
		t.Fatal("foreign avatar is deleted")
	}

	want := `
# HELP avatars_deletes_total Avatars deleted by users.
# TYPE avatars_deletes_total counter
avatars_deletes_total 1
# HELP avatars_uploads_total Avatar uploads by outcome: success, rejected (invalid file) or error (service failure).
# TYPE avatars_uploads_total counter
avatars_uploads_total{status="error"} 1
avatars_uploads_total{status="rejected"} 1
avatars_uploads_total{status="success"} 1
`
	if err := testutil.CollectAndCompare(reg, strings.NewReader(want), "avatars_uploads_total", "avatars_deletes_total"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(reg, "avatars_upload_duration_seconds"); n != 1 {
		t.Errorf("upload duration series = %d, want 1", n)
	}
}

// Без WithMetrics сервис работает как раньше.
func TestServiceWithoutMetrics(t *testing.T) {
	d := newDeps()
	if _, err := d.svc.Upload(context.Background(), uploadInput(encodePNG(t, 8, 8))); err != nil {
		t.Fatal(err)
	}
}
