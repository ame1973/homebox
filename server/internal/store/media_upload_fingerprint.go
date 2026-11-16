package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"homebox/internal/mediaimage"
)

// mediaUploadFingerprint binds the caller's original key, canonical declared
// MIME and exact raw bytes to the independent server secret. It does not grant
// authorization, validate image bytes, or create a receipt. The caller owns raw
// and must not change it during fingerprinting or subsequent sanitization.
func (a *Auth) mediaUploadFingerprint(ctx context.Context, key, mime string, raw []byte) (string, error) {
	if a == nil || len(a.serverSecret) != sha256.Size {
		return "", ErrAuthState
	}
	if ctx == nil || !authString(key, 8, 128) || !supportedMediaMIME(mime) || len(raw) < 1 || len(raw) > mediaimage.MaxInputBytes {
		return "", ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, a.serverSecret)
	_, _ = mac.Write([]byte("homebox-media-upload-v1\x00"))
	var length [8]byte
	for _, field := range []string{key, mime} {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(field))
	}
	binary.BigEndian.PutUint64(length[:], uint64(len(raw)))
	_, _ = mac.Write(length[:])
	for offset := 0; offset < len(raw); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := min(offset+(64<<10), len(raw))
		_, _ = mac.Write(raw[offset:end])
		offset = end
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
