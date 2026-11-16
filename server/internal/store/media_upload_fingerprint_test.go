package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"homebox/internal/mediaimage"
)

func uploadFingerprintFixture() *Auth {
	secret := make([]byte, 32)
	for index := range secret {
		secret[index] = byte(index)
	}
	return &Auth{serverSecret: secret}
}

func TestMediaUploadFingerprintBindsExactBytesMIMEKeyAndServerSecret(t *testing.T) {
	a := uploadFingerprintFixture()
	raw := []byte{'r', 'a', 'w', 0, 'i', 'm', 'a', 'g', 'e', 255}
	before := bytes.Clone(raw)
	original, err := a.mediaUploadFingerprint(t.Context(), "original-upload-key", "image/png", raw)
	if err != nil || original != "3d58389f61f44840b2a7c037219813cea239fa222b00d860f192def168c39f99" {
		t.Fatalf("incorrect stable keyed fingerprint: %q %v", original, err)
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("fingerprinting changed the source file")
	}
	plain := sha256.Sum256(raw)
	if original == hex.EncodeToString(plain[:]) {
		t.Fatal("fingerprint exposed a predictable plain file hash")
	}
	for _, item := range []struct {
		key, mime string
		file      []byte
		auth      *Auth
	}{
		{"different-upload-key", "image/png", raw, a},
		{"original-upload-key", "image/jpeg", raw, a},
		{"original-upload-key", "image/png", []byte("raw\x00image\xfe"), a},
		{"original-upload-key", "image/png", append(bytes.Clone(raw), 0), a},
		{"original-upload-key", "image/png", raw, &Auth{serverSecret: bytes.Repeat([]byte{7}, 32)}},
	} {
		other, err := item.auth.mediaUploadFingerprint(t.Context(), item.key, item.mime, item.file)
		if err != nil || other == original {
			t.Fatalf("different original intent or server produced the same fingerprint: %v", err)
		}
	}
	copyAuth := uploadFingerprintFixture()
	repeated, err := copyAuth.mediaUploadFingerprint(t.Context(), "original-upload-key", "image/png", raw)
	if err != nil || repeated != original {
		t.Fatal("identical original intent was not stable across authentication instances", err)
	}
}

func TestMediaUploadFingerprintRejectsInvalidInputsWithoutReturningAValue(t *testing.T) {
	a := uploadFingerprintFixture()
	for _, test := range []struct {
		name, key, mime string
		file            []byte
	}{
		{"short key", "short", "image/png", []byte("x")},
		{"long key", strings.Repeat("k", 129), "image/png", []byte("x")},
		{"invalid key Unicode", "12345678\xff", "image/png", []byte("x")},
		{"unsupported MIME", "valid-key", "image/svg+xml", []byte("x")},
		{"unnormalized MIME", "valid-key", "IMAGE/PNG", []byte("x")},
		{"MIME parameters", "valid-key", "image/png;charset=utf-8", []byte("x")},
		{"empty body", "valid-key", "image/png", nil},
		{"oversized body", "valid-key", "image/png", make([]byte, mediaimage.MaxInputBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := a.mediaUploadFingerprint(t.Context(), test.key, test.mime, test.file)
			if !errors.Is(err, ErrDomainInput) || value != "" {
				t.Fatal("invalid upload intent returned a fingerprint", err)
			}
		})
	}
	for _, invalid := range []*Auth{nil, {}, {serverSecret: make([]byte, 31)}} {
		if value, err := invalid.mediaUploadFingerprint(t.Context(), "valid-key", "image/png", []byte("x")); !errors.Is(err, ErrAuthState) || value != "" {
			t.Fatal("missing server secret produced a predictable fingerprint", err)
		}
	}
	if value, err := a.mediaUploadFingerprint(nil, "valid-key", "image/png", []byte("x")); !errors.Is(err, ErrDomainInput) || value != "" {
		t.Fatal("nil context returned a fingerprint", err)
	}
}

func TestMediaUploadFingerprintAcceptsExactRawCeilingAndCanonicalWebPDeclaration(t *testing.T) {
	raw := make([]byte, mediaimage.MaxInputBytes)
	raw[0], raw[len(raw)-1] = 1, 255
	value, err := uploadFingerprintFixture().mediaUploadFingerprint(t.Context(), "valid-key", "image/webp", raw)
	if err != nil || !validMediaDigest(value) || raw[0] != 1 || raw[len(raw)-1] != 255 {
		t.Fatal("exact raw-byte ceiling or supported declaration was rejected", err)
	}
}

type cancelFingerprintContext struct {
	context.Context
	remaining int
}

func (ctx *cancelFingerprintContext) Err() error {
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	ctx.remaining--
	return nil
}

func TestMediaUploadFingerprintHonorsCancellationWhileHashing(t *testing.T) {
	a := uploadFingerprintFixture()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{canceled, &cancelFingerprintContext{Context: t.Context(), remaining: 3}} {
		value, err := a.mediaUploadFingerprint(ctx, "valid-key", "image/png", make([]byte, 256<<10))
		if !errors.Is(err, context.Canceled) || value != "" {
			t.Fatal("canceled input returned a partial fingerprint", err)
		}
	}
}

func TestMediaUploadFingerprintDistinguishesRawFilesWithTheSameSanitizedPixels(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	var original bytes.Buffer
	if err := png.Encode(&original, img); err != nil {
		t.Fatal(err)
	}
	raw := original.Bytes()
	// Add a valid private ancillary chunk without changing decoded pixels.
	text := []byte("Comment\x00private GPS marker")
	chunk := make([]byte, len(text)+12)
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(text)))
	copy(chunk[4:8], "tEXt")
	copy(chunk[8:], text)
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	withMetadata := append(bytes.Clone(raw[:len(raw)-12]), chunk...)
	withMetadata = append(withMetadata, raw[len(raw)-12:]...)
	var sanitizedA, sanitizedB bytes.Buffer
	first, err := mediaimage.Sanitize(t.Context(), raw, "image/png", &sanitizedA, mediaimage.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mediaimage.Sanitize(t.Context(), withMetadata, "image/png", &sanitizedB, mediaimage.Limits{})
	if err != nil || first.SHA256 != second.SHA256 || !bytes.Equal(sanitizedA.Bytes(), sanitizedB.Bytes()) {
		t.Fatal("fixture did not produce the same sanitized image", err)
	}
	a := uploadFingerprintFixture()
	before, err := a.mediaUploadFingerprint(t.Context(), "original-upload-key", "image/png", raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := a.mediaUploadFingerprint(t.Context(), "original-upload-key", "image/png", withMetadata)
	if err != nil || after == before {
		t.Fatal("different raw private metadata collapsed into one request fingerprint", err)
	}
}

func TestMediaUploadFingerprintCanBindARealDomainReceiptAcrossSessions(t *testing.T) {
	s, a, _, _, token := completedAuthFixture(t)
	key, mime := "same-upload-key", "image/png"
	raw := []byte("raw image with captured-GPS-marker")
	fingerprint, err := a.mediaUploadFingerprint(t.Context(), key, mime, raw)
	if err != nil {
		t.Fatal(err)
	}
	nextAuth, err := s.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := nextAuth.mediaUploadFingerprint(t.Context(), key, mime, raw)
	if err != nil || restored != fingerprint {
		t.Fatal("a fresh authentication service did not retain the database secret", err)
	}
	applied := 0
	apply := func(m *mutationTx) (ActionAck, error) {
		applied++
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	}
	// This exercises the real receipt boundary, not a Media producer or upload.
	first, err := runDomainMutation(t.Context(), a, token, key, "post_media", mediaUploadReceiptRequest{RawFileMAC: fingerprint}, apply)
	if err != nil || !first.OK || applied != 1 {
		t.Fatal("could not bind the first receipt", err)
	}
	if scalar[int](t, s.db, "SELECT COUNT(*) FROM auth_keyed_receipt_secret") != 1 {
		t.Fatal("keyed receipt did not pin its server secret")
	}
	_, nextToken := login(t, a)
	replayed, err := runDomainMutation(t.Context(), a, nextToken, key, "post_media", mediaUploadReceiptRequest{RawFileMAC: fingerprint}, apply)
	if err != nil || replayed != first || applied != 1 {
		t.Fatal("same original upload intent did not replay across sessions", err)
	}
	changed, err := a.mediaUploadFingerprint(t.Context(), key, mime, append(bytes.Clone(raw), '!'))
	if err != nil {
		t.Fatal(err)
	}
	if value, err := runDomainMutation(t.Context(), a, nextToken, key, "post_media", mediaUploadReceiptRequest{RawFileMAC: changed}, apply); !errors.Is(err, ErrIdempotencyConflict) || value != (ActionAck{}) || applied != 1 {
		t.Fatal("changed original raw bytes were accepted under the same key", err)
	}
	for _, query := range []string{
		"SELECT request_sha256 FROM api_idempotency WHERE operation='post_media'",
		"SELECT response_json FROM api_idempotency WHERE operation='post_media'",
		"SELECT result_json FROM mutation_events WHERE operation='post_media'",
	} {
		if strings.Contains(scalar[string](t, s.db, query), "captured-GPS-marker") {
			t.Fatal("private input metadata entered a durable receipt or audit result")
		}
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := reopened.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = a.now
	afterRestart, err := restarted.mediaUploadFingerprint(t.Context(), key, mime, raw)
	if err != nil || afterRestart != fingerprint {
		t.Fatal("a real store restart changed the original fingerprint", err)
	}
	_, restartToken := login(t, restarted)
	replayed, err = runDomainMutation(t.Context(), restarted, restartToken, key, "post_media", mediaUploadReceiptRequest{RawFileMAC: afterRestart}, apply)
	if err != nil || replayed != first || applied != 1 {
		t.Fatal("receipt could not be reconciled after restart and fresh login", err)
	}
}

func FuzzMediaUploadFingerprint(f *testing.F) {
	f.Add([]byte("raw\x00image"), "image/png", "original-key")
	f.Add([]byte{0, 255}, "image/webp", "😀😀😀😀😀😀😀😀")
	f.Fuzz(func(t *testing.T, raw []byte, mime, key string) {
		if len(raw) == 0 || len(raw) > 8192 || !supportedMediaMIME(mime) || !authString(key, 8, 128) {
			t.Skip()
		}
		a := uploadFingerprintFixture()
		before := bytes.Clone(raw)
		first, err := a.mediaUploadFingerprint(t.Context(), key, mime, raw)
		if err != nil || !validMediaDigest(first) || !bytes.Equal(raw, before) {
			t.Fatal("valid bounded original input failed", err)
		}
		repeat, err := a.mediaUploadFingerprint(t.Context(), key, mime, raw)
		if err != nil || repeat != first {
			t.Fatal("fingerprint changed without changed input", err)
		}
		changed := bytes.Clone(raw)
		changed[0] ^= 0xff
		other, err := a.mediaUploadFingerprint(t.Context(), key, mime, changed)
		if err != nil || other == first {
			t.Fatal("changed raw bytes did not change the keyed fingerprint", err)
		}
	})
}
