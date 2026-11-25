package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// syncSearchProof binds exact frozen bytes to a server instance, dataset and
// change identity. It does not validate a projection, establish reviewed source
// or authorize a read. The caller must own an unchanged payload and verify the
// actual database signer and source in the publication transaction.
func (a *Auth) syncSearchProof(ctx context.Context, instance, epoch, identity string, seq, revision int64, payload []byte) ([32]byte, error) {
	var result [32]byte
	if a == nil || len(a.serverSecret) != sha256.Size {
		return result, ErrAuthState
	}
	if ctx == nil || !validMediaInstance(instance) || !authString(epoch, 1, 128) || !validProjectionIdentity("search_doc", identity) ||
		seq < 1 || seq > maxSafeInteger || revision < 1 || revision > maxSafeInteger || len(payload) == 0 {
		return result, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	mac := hmac.New(sha256.New, a.serverSecret)
	_, _ = mac.Write([]byte("homebox-sync-search-proof-v1\x00"))
	var number [8]byte
	for _, field := range []string{instance, epoch, identity} {
		binary.BigEndian.PutUint64(number[:], uint64(len(field)))
		_, _ = mac.Write(number[:])
		_, _ = mac.Write([]byte(field))
	}
	for _, value := range []uint64{uint64(seq), uint64(revision), uint64(len(payload))} {
		binary.BigEndian.PutUint64(number[:], value)
		_, _ = mac.Write(number[:])
	}
	for offset := 0; offset < len(payload); {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		end := offset + min(64<<10, len(payload)-offset)
		_, _ = mac.Write(payload[offset:end])
		offset = end
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	copy(result[:], mac.Sum(nil))
	return result, nil
}
