package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

// These proofs bind exact bytes to a snapshot, not their source, completeness,
// or an authorized session. The caller must verify the actual database signer
// and source before signing and must own an unchanged payload while hashing.
func (a *Auth) syncSnapshotProofMAC(ctx context.Context, cursor syncSnapshotCursor, total int64, domain string) (hash.Hash, error) {
	if a == nil || len(a.serverSecret) != sha256.Size {
		return nil, ErrAuthState
	}
	if ctx == nil || !validSyncSnapshotCursor(cursor) || cursor.Scope.ProjectionVersion != 1 ||
		total < 0 || total > maxSafeInteger || cursor.NextOrdinal > total {
		return nil, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, a.serverSecret)
	_, _ = mac.Write([]byte(domain))
	writeSnapshotProofStrings(mac, cursor.Scope.AdminID, cursor.Scope.ServerInstanceID, cursor.Scope.DatasetEpoch,
		cursor.SnapshotID, cursor.CreatedAt, cursor.ExpiresAt)
	writeSnapshotProofNumbers(mac, uint64(cursor.Version), uint64(cursor.Scope.ProjectionVersion), uint64(cursor.HighWatermark), uint64(total))
	return mac, nil
}

func writeSnapshotProofStrings(mac hash.Hash, fields ...string) {
	for _, field := range fields {
		writeSnapshotProofNumbers(mac, uint64(len(field)))
		_, _ = mac.Write([]byte(field))
	}
}

func writeSnapshotProofNumbers(mac hash.Hash, values ...uint64) {
	var number [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(number[:], value)
		_, _ = mac.Write(number[:])
	}
}

func (a *Auth) syncSnapshotManifestProof(ctx context.Context, cursor syncSnapshotCursor, total int64) ([32]byte, error) {
	var result [32]byte
	mac, err := a.syncSnapshotProofMAC(ctx, cursor, total, "homebox-sync-snapshot-manifest-v1\x00")
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	copy(result[:], mac.Sum(nil))
	return result, nil
}

func (a *Auth) syncSnapshotRowProof(ctx context.Context, cursor syncSnapshotCursor, total, ordinal int64, kind, identity string, revision int64, payload []byte) ([32]byte, error) {
	var result [32]byte
	mac, err := a.syncSnapshotProofMAC(ctx, cursor, total, "homebox-sync-snapshot-row-v1\x00")
	if err != nil {
		return result, err
	}
	if ordinal < 0 || ordinal >= total || !validProjectionIdentity(kind, identity) || revision < 1 || revision > maxSafeInteger ||
		len(payload) == 0 || len(payload) > maxSyncPageBytes {
		return result, ErrDomainInput
	}
	writeSnapshotProofStrings(mac, kind, identity)
	writeSnapshotProofNumbers(mac, uint64(ordinal), uint64(revision), uint64(len(payload)))
	for offset := 0; offset < len(payload); {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		end := offset + min(64<<10, len(payload)-offset)
		_, _ = mac.Write(payload[offset:end])
		offset = end
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	copy(result[:], mac.Sum(nil))
	return result, nil
}
