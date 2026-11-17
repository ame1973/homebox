//go:build linux

package store

import "context"

// recoverAuditedMediaBatch runs a non-destructive staging audit and then one
// recovery page under the same storage owner lock. An audit error happens before
// any unlink. A later recovery error may follow durable filesystem changes or a
// committed prefix and requires explicit retry; more is meaningful only on
// success. This is not a scheduler or an HTTP authorization boundary.
func (storage *mediaStorage) recoverAuditedMediaBatch(ctx context.Context, limit int) (completed int, more bool, finalErr error) {
	if storage == nil || storage.store == nil || storage.store.db == nil || ctx == nil || limit < 1 || limit > 100 {
		return 0, false, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if _, err := storage.auditMediaStagingLocked(ctx, maxMediaStagingAuditEntries); err != nil {
		return 0, false, err
	}
	return storage.recoverPendingMediaBatchLocked(ctx, limit)
}
