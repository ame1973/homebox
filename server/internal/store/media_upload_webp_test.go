package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// This lossless 2x3 alpha raster was independently produced by Pillow/libwebp.
const uploadWebPFixture = "UklGRjYAAABXRUJQVlA4TCkAAAAvAYAAEC8w/wKCIv9HExAU+T+aQIDgzP+U4A0i6igIBNKQUgorHNH/WAA="

func privateUploadWebP(t *testing.T, marker string) []byte {
	t.Helper()
	image, err := base64.StdEncoding.DecodeString(uploadWebPFixture)
	if err != nil {
		t.Fatal(err)
	}
	exif := make([]byte, 44+len(marker))
	copy(exif, "II")
	binary.LittleEndian.PutUint16(exif[2:4], 42)
	binary.LittleEndian.PutUint32(exif[4:8], 8)
	binary.LittleEndian.PutUint16(exif[8:10], 2)
	binary.LittleEndian.PutUint16(exif[10:12], 0x0112)
	binary.LittleEndian.PutUint16(exif[12:14], 3)
	binary.LittleEndian.PutUint32(exif[14:18], 1)
	binary.LittleEndian.PutUint16(exif[18:20], 1)
	binary.LittleEndian.PutUint16(exif[22:24], 0x8825)
	binary.LittleEndian.PutUint16(exif[24:26], 4)
	binary.LittleEndian.PutUint32(exif[26:30], 1)
	binary.LittleEndian.PutUint32(exif[30:34], 38)
	copy(exif[44:], marker)
	chunk := func(name string, data []byte) []byte {
		value := make([]byte, 8+len(data)+(len(data)&1))
		copy(value, name)
		binary.LittleEndian.PutUint32(value[4:8], uint32(len(data)))
		copy(value[8:], data)
		return value
	}
	out := append([]byte("RIFF\x00\x00\x00\x00WEBP"), chunk("VP8X", []byte{0x18, 0, 0, 0, 1, 0, 0, 2, 0, 0})...)
	out = append(out, image[12:]...)
	out = append(out, chunk("EXIF", exif)...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

func TestStagedMediaUploadSanitizesWebPAndReconcilesExactRawRequests(t *testing.T) {
	s, _, library, token, staging, objects := privateUploadFixture(t)
	firstRaw := privateUploadWebP(t, "private GPS marker A")
	secondRaw := privateUploadWebP(t, "private GPS marker B")
	var first Media
	changed := requireProjectionReplay(t, s, func() {
		var err error
		first, err = library.uploadStagedMedia(t.Context(), token, "webp-original-key", "image/webp", firstRaw, staging, objects)
		if err != nil {
			t.Fatal("real WebP staged upload failed", err)
		}
	})
	if len(changed) != 1 || !changed[projectionKey{"media", first.ID}] || first.Revision != 1 || first.Verification != "verified" ||
		first.MIMEType != "image/png" || first.Width != 2 || first.Height != 3 {
		t.Fatal("WebP upload did not publish its actual sanitized PNG", first, changed)
	}
	source, err := loadMedia(t.Context(), s.db, first.ID)
	if err != nil || source.thumbnail == nil {
		t.Fatal("WebP publication lacks its actual thumbnail source", err)
	}
	full, err := os.ReadFile(filepath.Join(objects, source.storageKey))
	if err != nil || int64(len(full)) != source.Bytes || sha256.Sum256(full) != mustDecodeMediaSHA(t, source.SHA256) || bytes.Contains(full, []byte("private GPS marker")) {
		t.Fatal("WebP sanitized object is invalid or retained private metadata", err)
	}
	if _, err = png.Decode(bytes.NewReader(full)); err != nil {
		t.Fatal("WebP source was not re-encoded to actual PNG", err)
	}
	thumb, err := os.ReadFile(filepath.Join(objects, "sha256-"+source.thumbnail.SHA256))
	if err != nil || sha256.Sum256(thumb) != mustDecodeMediaSHA(t, source.thumbnail.SHA256) || bytes.Contains(thumb, []byte("private GPS marker")) {
		t.Fatal("WebP thumbnail retained private metadata or disagreed with its source", err)
	}
	if _, err = jpeg.Decode(bytes.NewReader(thumb)); err != nil {
		t.Fatal("WebP thumbnail is not a JPEG", err)
	}
	requireEmptyUploadStages(t, staging)
	replay, err := library.uploadStagedMedia(t.Context(), token, "webp-original-key", "image/webp", firstRaw, staging, objects)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatal("exact WebP request could not reconcile its original key", replay, err)
	}
	if value, err := library.uploadStagedMedia(t.Context(), token, "webp-original-key", "image/webp", secondRaw, staging, objects); !errors.Is(err, ErrIdempotencyConflict) || value != (Media{}) {
		t.Fatal("different raw private metadata reused an original WebP key", value, err)
	}
	second, err := library.uploadStagedMedia(t.Context(), token, "webp-different-key", "image/webp", secondRaw, staging, objects)
	if err != nil || !reflect.DeepEqual(first, second) || scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 1 {
		t.Fatal("same sanitized pixels were not deduplicated independently of raw request identity", second, err)
	}
	requireEmptyUploadStages(t, staging)
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_links") != 0 {
		t.Fatal("WebP publication retained pending rows or silently attached an owner")
	}
}
