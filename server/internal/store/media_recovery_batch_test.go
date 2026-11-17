//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestMediaRecoveryBatchCompletesBoundedActualRowsWithoutTouchingSources(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	published, err := f.storage.upload(t.Context(), f.library, f.token, "batch-published-upload", "image/png", privateUploadPNG(t, "private"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := loadMedia(t.Context(), f.store.db, published.ID)
	if err != nil || actual.thumbnail == nil {
		t.Fatal("missing published source", err)
	}
	before := domainState(t, f.store)
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 4 {
		t.Fatal("fixture did not retain two abandoned and two published stages", count)
	}
	completed, more, err := f.storage.recoverPendingMediaBatch(t.Context(), 1)
	if err != nil || completed != 1 || !more {
		t.Fatal("bounded first batch did not signal remaining work", completed, more, err)
	}
	completed, more, err = f.storage.recoverPendingMediaBatch(t.Context(), 2)
	if err != nil || completed != 2 || !more {
		t.Fatal("bounded second batch did not retain the continuation", completed, more, err)
	}
	completed, more, err = f.storage.recoverPendingMediaBatch(t.Context(), 2)
	if err != nil || completed != 1 || more {
		t.Fatal("final batch misreported completion", completed, more, err)
	}
	completed, more, err = f.storage.recoverPendingMediaBatch(t.Context(), 2)
	if err != nil || completed != 0 || more {
		t.Fatal("empty batch did not terminate", completed, more, err)
	}
	requireDomainState(t, f.store, before)
	current, err := loadMedia(t.Context(), f.store.db, published.ID)
	if err != nil || !reflect.DeepEqual(current, actual) {
		t.Fatal("batch changed the published source or private thumbnail", err)
	}
	if scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("completed batches retained pending rows")
	}
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err = os.Stat(filepath.Join(f.storage.path, "objects", key)); err != nil {
			t.Fatal("batch cleanup removed a referenced object", err)
		}
	}
	if _, err = os.Stat(f.objectPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("batch did not remove the proven unreferenced promoted object", err)
	}
}

func TestMediaRecoveryBatchStopsOnFailureAndRetriesOnlyExplicitly(t *testing.T) {
	f := newRecoveryFixture(t)
	ids, next, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 || next != "" {
		t.Fatal("fixture pending page failed", ids, next, err)
	}
	execSQL(t, f.store.db, fmt.Sprintf(`CREATE TRIGGER batch_failure BEFORE DELETE ON media_pending_objects WHEN OLD.id='%s' BEGIN SELECT RAISE(ABORT,'private failure'); END`, ids[1]))
	before := domainState(t, f.store)
	completed, _, err := f.storage.recoverPendingMediaBatch(t.Context(), 2)
	if err == nil || completed != 1 {
		t.Fatal("batch skipped a failed object or lost its completed prefix", completed, err)
	}
	if scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", ids[0]) != 0 ||
		scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", ids[1]) != 1 {
		t.Fatal("failure changed the wrong pending rows")
	}
	requireDomainState(t, f.store, before)
	execSQL(t, f.store.db, "DROP TRIGGER batch_failure")
	completed, more, err := f.storage.recoverPendingMediaBatch(t.Context(), 2)
	if err != nil || completed != 1 || more || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("explicit retry did not reconcile a possibly removed stage", completed, more, err)
	}
	requireDomainState(t, f.store, before)
}

func TestMediaRecoveryBatchPreservesExactPendingIdentities(t *testing.T) {
	f := newRecoveryFixture(t)
	execSQL(t, f.store.db, "DROP TRIGGER media_pending_objects_no_update")
	for index, id := range []string{"a\x00b", "a\x00c"} {
		execSQL(t, f.store.db, "UPDATE media_pending_objects SET id=? WHERE id=?", id, f.pending[index].ID)
	}
	completed, more, err := f.storage.recoverPendingMediaBatch(t.Context(), 1)
	if err != nil || completed != 1 || !more || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", "a\x00c") != 1 {
		t.Fatal("first batch changed an exact retained identity", completed, more, err)
	}
	completed, more, err = f.storage.recoverPendingMediaBatch(t.Context(), 100)
	if err != nil || completed != 1 || more {
		t.Fatal("second batch failed to process the exact retained identity", completed, more, err)
	}
}

func TestMediaRecoveryBatchRejectsCorruptLookaheadBeforeAnyUnlink(t *testing.T) {
	f := newRecoveryFixture(t)
	ids, _, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 {
		t.Fatal("fixture pending page failed", ids, err)
	}
	execSQL(t, f.store.db, "DROP TRIGGER media_pending_objects_no_update")
	execSQL(t, f.store.db, "UPDATE media_pending_objects SET stage_name='../outside' WHERE id=?", ids[1])
	selected, err := loadMediaPendingRow(t.Context(), f.store.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	completed, _, err := f.storage.recoverPendingMediaBatch(t.Context(), 1)
	if err == nil || completed != 0 {
		t.Fatal("corrupt lookahead allowed a recovered prefix", completed, err)
	}
	if _, err = os.Stat(filepath.Join(f.storage.path, "staging", selected.StageName)); err != nil ||
		scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
		t.Fatal("corrupt lookahead changed source files or journal", err)
	}
}

func TestMediaRecoveryBatchSerializesAndRejectsInvalidCalls(t *testing.T) {
	var missing *mediaStorage
	if completed, _, err := missing.recoverPendingMediaBatch(t.Context(), 1); !errors.Is(err, ErrDomainInput) || completed != 0 {
		t.Fatal("nil storage was accepted", completed, err)
	}
	f := newRecoveryFixture(t)
	for _, limit := range []int{-1, 0, 101} {
		if completed, _, err := f.storage.recoverPendingMediaBatch(t.Context(), limit); !errors.Is(err, ErrDomainInput) || completed != 0 {
			t.Fatal("invalid recovery batch limit was accepted", limit, completed, err)
		}
	}
	if completed, _, err := f.storage.recoverPendingMediaBatch(nil, 1); !errors.Is(err, ErrDomainInput) || completed != 0 {
		t.Fatal("nil context was accepted", completed, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if completed, _, err := f.storage.recoverPendingMediaBatch(ctx, 1); !errors.Is(err, context.Canceled) || completed != 0 {
		t.Fatal("canceled batch began work", completed, err)
	}
	f.storage.mu.Lock()
	waiting, stopWaiting := context.WithCancel(t.Context())
	canceled := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); _, _, err := f.storage.recoverPendingMediaBatch(waiting, 2); canceled <- err }()
	<-started
	select {
	case err := <-canceled:
		f.storage.mu.Unlock()
		t.Fatal("batch returned while the storage lock was held", err)
	case <-time.After(100 * time.Millisecond):
	}
	stopWaiting()
	f.storage.mu.Unlock()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
			t.Fatal("canceled lock waiter changed pending state", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled batch did not return after unlocking")
	}
	f.storage.mu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := f.storage.recoverPendingMediaBatch(t.Context(), 2); done <- err }()
	select {
	case err := <-done:
		f.storage.mu.Unlock()
		t.Fatal("batch did not wait for the storage owner", err)
	case <-time.After(100 * time.Millisecond):
	}
	f.storage.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("batch failed after ownership became available", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not resume after the lock was released")
	}
	if err := f.storage.Close(); err != nil {
		t.Fatal(err)
	}
	if completed, _, err := f.storage.recoverPendingMediaBatch(t.Context(), 1); !errors.Is(err, errMediaStorageClosed) || completed != 0 {
		t.Fatal("closed storage accepted a batch", completed, err)
	}
}
