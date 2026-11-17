//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAuditedMediaRecoveryStopsBeforeDeletingAnyUnknownStage(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	unknown := filepath.Join(f.storage.path, "staging", "unknown-private-entry")
	if err := os.WriteFile(unknown, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, f.store)
	var stageBefore, objectBefore unix.Stat_t
	if err := unix.Lstat(f.stagePath(0), &stageBefore); err != nil {
		t.Fatal(err)
	}
	if err := unix.Lstat(f.objectPath(0), &objectBefore); err != nil {
		t.Fatal(err)
	}
	completed, more, err := f.storage.recoverAuditedMediaBatch(t.Context(), 2)
	if completed != 0 || more || !errors.Is(err, errMediaStagingUnproven) {
		t.Fatal("unknown staging entry did not block recovery", completed, more, err)
	}
	var stageAfter, objectAfter unix.Stat_t
	if err = unix.Lstat(f.stagePath(0), &stageAfter); err != nil || !sameMediaFile(stageBefore, stageAfter) {
		t.Fatal("audit failure changed the proven stage", err)
	}
	if err = unix.Lstat(f.objectPath(0), &objectAfter); err != nil || !sameMediaFile(objectBefore, objectAfter) {
		t.Fatal("audit failure changed the proven object", err)
	}
	if value, err := os.ReadFile(unknown); err != nil || string(value) != "do not delete" {
		t.Fatal("audit failure changed the unrelated file", string(value), err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("audit failure changed pending journal rows", count)
	}
	requireDomainState(t, f.store, before)
	// This is a test-owned unrelated file, inspected above before removal.
	if err = os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	completed, more, err = f.storage.recoverAuditedMediaBatch(t.Context(), 2)
	if err != nil || completed != 2 || more {
		t.Fatal("explicit retry did not recover the verified stages", completed, more, err)
	}
	if _, err = os.Stat(f.objectPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreferenced proven object remained after recovery", err)
	}
	requireDomainState(t, f.store, before)
}

func TestAuditedMediaRecoveryStopsAndExplicitlyRetriesPartialFailure(t *testing.T) {
	f := newRecoveryFixture(t)
	ids, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 || next != "" {
		t.Fatal("pending fixture failed", ids, next, err)
	}
	execSQL(t, f.store.db, fmt.Sprintf(`CREATE TRIGGER audited_batch_failure BEFORE DELETE ON media_pending_objects WHEN OLD.id='%s' BEGIN SELECT RAISE(ABORT,'private failure'); END`, ids[1]))
	before := domainState(t, f.store)
	completed, _, err := f.storage.recoverAuditedMediaBatch(t.Context(), 2)
	if err == nil || completed != 1 {
		t.Fatal("batch did not stop after the first failed object", completed, err)
	}
	if scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", ids[0]) != 0 ||
		scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", ids[1]) != 1 {
		t.Fatal("partial failure changed the wrong journal rows")
	}
	requireDomainState(t, f.store, before)
	execSQL(t, f.store.db, "DROP TRIGGER audited_batch_failure")
	completed, more, err := f.storage.recoverAuditedMediaBatch(t.Context(), 2)
	if err != nil || completed != 1 || more || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("explicit retry did not reconcile the remaining object", completed, more, err)
	}
	requireDomainState(t, f.store, before)
}

func TestAuditedMediaRecoveryRechecksActualBytesBeforeUnlink(t *testing.T) {
	f := newRecoveryFixture(t)
	ids, _, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 {
		t.Fatal("pending fixture failed", ids, err)
	}
	row, err := loadMediaPendingRow(t.Context(), f.store.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.storage.path, "staging", row.StageName)
	value, err := os.ReadFile(path)
	if err != nil || len(value) == 0 {
		t.Fatal("stage fixture failed", err)
	}
	value[0] ^= 1
	if err = os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
	completed, more, err := f.storage.recoverAuditedMediaBatch(t.Context(), 2)
	if completed != 0 || more || !errors.Is(err, errMediaRecoveryUnproven) {
		t.Fatal("metadata audit bypassed actual byte verification", completed, more, err)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != string(value) {
		t.Fatal("failed recovery changed the altered stage", err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("failed byte verification changed journal rows", count)
	}
}

func TestAuditedMediaRecoveryKeepsTheStorageLockAcrossBothPhases(t *testing.T) {
	f := newRecoveryFixture(t)
	held, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	type batchResult struct {
		completed int
		more      bool
		err       error
	}
	batch := make(chan batchResult, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		completed, more, err := f.storage.recoverAuditedMediaBatch(t.Context(), 2)
		batch <- batchResult{completed, more, err}
	}()
	<-started
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if !f.storage.mu.TryLock() {
			break
		}
		f.storage.mu.Unlock()
		select {
		case <-deadline.C:
			t.Fatal("audited recovery never acquired the owner lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	type pageResult struct {
		ids  []string
		next string
		err  error
	}
	page := make(chan pageResult, 1)
	go func() {
		ids, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 10)
		page <- pageResult{ids, next, err}
	}()
	select {
	case result := <-page:
		t.Fatal("another owner operation ran during audit", result)
	case <-time.After(100 * time.Millisecond):
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-batch:
		if result.err != nil || result.completed != 2 || result.more {
			t.Fatal("audited recovery did not finish after database release", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("audited recovery remained blocked")
	}
	select {
	case result := <-page:
		if result.err != nil || len(result.ids) != 0 || result.next != "" {
			t.Fatal("another owner operation observed a partial batch", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner operation did not resume after recovery")
	}
}

func TestAuditedMediaRecoveryBoundsAndReferencedObjects(t *testing.T) {
	var missing *mediaStorage
	if completed, more, err := missing.recoverAuditedMediaBatch(t.Context(), 1); completed != 0 || more || !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil storage was accepted", completed, more, err)
	}
	f := newRecoveryFixture(t)
	for _, limit := range []int{-1, 0, 101} {
		if completed, more, err := f.storage.recoverAuditedMediaBatch(t.Context(), limit); completed != 0 || more || !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid batch limit was accepted", limit, completed, more, err)
		}
	}
	if completed, more, err := f.storage.recoverAuditedMediaBatch(nil, 1); completed != 0 || more || !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context was accepted", completed, more, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if completed, more, err := f.storage.recoverAuditedMediaBatch(ctx, 1); completed != 0 || more || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled recovery began work", completed, more, err)
	}
	published, err := f.storage.upload(t.Context(), f.library, f.token, "audited-published-media", "image/png", privateUploadPNG(t, "private"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := loadMedia(t.Context(), f.store.db, published.ID)
	if err != nil || actual.thumbnail == nil {
		t.Fatal("published source fixture failed", err)
	}
	before := domainState(t, f.store)
	completed, more, err := f.storage.recoverAuditedMediaBatch(t.Context(), 1)
	if err != nil || completed != 1 || !more {
		t.Fatal("first bounded audited batch did not retain continuation", completed, more, err)
	}
	completed, more, err = f.storage.recoverAuditedMediaBatch(t.Context(), 10)
	if err != nil || completed != 3 || more {
		t.Fatal("remaining audited batch did not complete", completed, more, err)
	}
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err = os.Stat(filepath.Join(f.storage.path, "objects", key)); err != nil {
			t.Fatal("audited recovery removed a referenced object", err)
		}
	}
	requireDomainState(t, f.store, before)
	if err = f.storage.Close(); err != nil {
		t.Fatal(err)
	}
	if completed, more, err = f.storage.recoverAuditedMediaBatch(t.Context(), 1); completed != 0 || more || !errors.Is(err, errMediaStorageClosed) {
		t.Fatal("closed storage accepted audited recovery", completed, more, err)
	}
}
