package services

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"

	"go-avatar-service/internal/domain"
)

// pngWithSize — PNG 1x1, у которого в заголовке IHDR записан размер w×h.
// DecodeConfig читает только заголовок, поэтому такой файл «весит» байты,
// а при декодировании развернулся бы в w×h пикселей.
func pngWithSize(t *testing.T, w, h uint32) []byte {
	t.Helper()
	data := encodePNG(t, 1, 1)
	// 8 байт сигнатуры, 4 — длина чанка, 4 — "IHDR", затем ширина и высота.
	const ihdrData = 16
	binary.BigEndian.PutUint32(data[ihdrData:], w)
	binary.BigEndian.PutUint32(data[ihdrData+4:], h)
	crc := crc32.ChecksumIEEE(data[12 : ihdrData+13])
	binary.BigEndian.PutUint32(data[ihdrData+13:], crc)
	return data
}

func TestAvatarServiceUploadStrictChecks(t *testing.T) {
	tests := []struct {
		name        string
		data        func(t *testing.T) []byte
		contentType string
		wantErr     error
		wantDetails string
	}{
		{
			name:        "declared type matches",
			data:        func(t *testing.T) []byte { return encodePNG(t, 4, 4) },
			contentType: "image/png",
		},
		{
			name:        "octet-stream says nothing",
			data:        func(t *testing.T) []byte { return encodeJPEG(t, 4, 4) },
			contentType: "application/octet-stream",
		},
		{
			name:        "declared type with parameters",
			data:        func(t *testing.T) []byte { return encodeJPEG(t, 4, 4) },
			contentType: "IMAGE/JPEG; name=me.jpg",
		},
		{
			name:        "declared type differs from content",
			data:        func(t *testing.T) []byte { return encodeJPEG(t, 4, 4) },
			contentType: "image/png",
			wantDetails: `file content is image/jpeg, but Content-Type is "image/png"`,
		},
		{
			name:        "malformed declared type",
			data:        func(t *testing.T) []byte { return encodePNG(t, 4, 4) },
			contentType: "image/png;;;=",
			wantDetails: `file content is image/png, but Content-Type is "image/png;;;="`,
		},
		{
			name:        "decompression bomb",
			data:        func(t *testing.T) []byte { return pngWithSize(t, 20000, 20000) },
			wantDetails: "image is 20000x20000, at most 50 megapixels are allowed",
		},
		{
			name:    "magic bytes without the rest",
			data:    func(*testing.T) []byte { return []byte{0xFF, 0xD8, 0xFF, 0xE0} },
			wantErr: domain.ErrInvalidFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDeps()
			in := uploadInput(tt.data(t))
			in.ContentType = tt.contentType

			_, err := d.svc.Upload(context.Background(), in)

			var verr *domain.ValidationError
			switch {
			case tt.wantDetails != "":
				if !errors.As(err, &verr) || verr.Details != tt.wantDetails {
					t.Fatalf("err = %v, want validation error %q", err, tt.wantDetails)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Upload: %v", err)
				}
				return
			}
			if len(*d.log) != 0 {
				t.Errorf("rejected file reached dependencies: %v", *d.log)
			}
		})
	}
}

func TestSupportedFormatsMagic(t *testing.T) {
	tests := []struct {
		mime   string
		header []byte
		want   bool
	}{
		{"image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xDB}, true},
		{"image/jpeg", []byte{0xFF, 0xD8}, false},
		{"image/png", []byte("\x89PNG\r\n\x1a\nrest"), true},
		{"image/png", []byte("\x89PNG"), false},
		{"image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8L"), true},
		{"image/webp", []byte("RIFF\x00\x00\x00\x00WAVE"), false},
	}
	for _, tt := range tests {
		if got := supportedFormats[tt.mime].magic(tt.header); got != tt.want {
			t.Errorf("%s magic(%q) = %v, want %v", tt.mime, tt.header, got, tt.want)
		}
	}
}
