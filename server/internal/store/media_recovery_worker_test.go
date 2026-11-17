//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitMediaRecoveryWorker(t *testing.T, worker *mediaRecoveryWorker, accept func(mediaRecoveryWorkerStatus) bool) mediaRecoveryWorkerStatus {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := worker.snapshot()
		if accept(status) {
			return status
		}
		select {
		case <-deadline.C:
			t.Fatal("media recovery worker did not reach the expected state", status)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-ticker.C:
		}
	}
}

func testMediaRecoveryWorker(t *testing.T, storage *mediaStorage) *mediaRecoveryWorker {
	t.Helper()
	worker, err := startMediaRecoveryWorker(t.Context(), storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := worker.close(); err != nil {
			t.Error(err)
		}
	})
	return worker
}

func TestMediaRecoveryWorkerDrainsBoundedStartupBacklogAndLaterNotifications(t *testing.T) {
	f := newRecoveryFixture(t)
	var published Media
	for index := 0; index < 5; index++ {
		value, err := f.storage.upload(t.Context(), f.library, f.token, fmt.Sprintf("worker-seed-%d", index), "image/png", privateUploadPNG(t, "private"))
		if err != nil {
			t.Fatal(err)
		}
		published = value
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 12 {
		t.Fatal("worker fixture did not span multiple batches", count)
	}
	actual, err := loadMedia(t.Context(), f.store.db, published.ID)
	if err != nil || actual.thumbnail == nil {
		t.Fatal("published source fixture failed", err)
	}
	before := domainState(t, f.store)
	worker := testMediaRecoveryWorker(t, f.storage)
	status := waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 12
	})
	if status.Failure != mediaRecoveryFailureNone || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("startup recovery did not drain the journal", status)
	}
	requireDomainState(t, f.store, before)
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err = os.Stat(filepath.Join(f.storage.path, "objects", key)); err != nil {
			t.Fatal("worker removed a referenced object", err)
		}
	}
	value, err := f.storage.upload(t.Context(), f.library, f.token, "worker-later-upload", "image/png", privateUploadPNG(t, "private"))
	if err != nil || value.ID != published.ID {
		t.Fatal("later upload fixture failed", value, err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("later upload did not leave the expected journal", count)
	}
	if err = worker.notify(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 14
	})
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("notification did not trigger cleanup", count)
	}
	replay, err := f.storage.upload(t.Context(), f.library, f.token, "worker-later-upload", "image/png", privateUploadPNG(t, "private"))
	if err != nil || replay != value {
		t.Fatal("cleanup changed original-key reconciliation", replay, err)
	}
}

func TestMediaRecoveryWorkerLatchesUnknownStageUntilExplicitRetry(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	unknown := filepath.Join(f.storage.path, "staging", "private-unknown-stage")
	if err := os.WriteFile(unknown, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, f.store)
	worker := testMediaRecoveryWorker(t, f.storage)
	status := waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerBlocked })
	if status.Failure != mediaRecoveryFailureStagingUnproven || status.Completed != 0 || strings.Contains(fmt.Sprint(status), "private-unknown-stage") {
		t.Fatal("unknown-stage status was not bounded and non-sensitive", status)
	}
	if _, err := os.Stat(f.stagePath(0)); err != nil {
		t.Fatal("blocked worker removed a journaled stage", err)
	}
	if _, err := os.Stat(f.objectPath(0)); err != nil {
		t.Fatal("blocked worker removed a journaled object", err)
	}
	if value, err := os.ReadFile(unknown); err != nil || string(value) != "do not delete" {
		t.Fatal("blocked worker changed the unknown file", string(value), err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	if err := worker.notify(); !errors.Is(err, errMediaRecoveryWorkerBlocked) {
		t.Fatal("an ordinary notification silently retried a blocked worker", err)
	}
	time.Sleep(150 * time.Millisecond)
	if status = worker.snapshot(); status.Phase != mediaRecoveryWorkerBlocked || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
		t.Fatal("blocked worker retried without an explicit action", status)
	}
	if err := worker.retry(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
	if _, err := os.Stat(f.objectPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("explicit retry did not remove the proven unreferenced object", err)
	}
	requireDomainState(t, f.store, before)
}

func TestMediaRecoveryWorkerLatchesUnprovenStageBytes(t *testing.T) {
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
	original, err := os.ReadFile(path)
	if err != nil || len(original) == 0 {
		t.Fatal("stage fixture failed", err)
	}
	changed := append([]byte(nil), original...)
	changed[0] ^= 1
	if err = os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	worker := testMediaRecoveryWorker(t, f.storage)
	status := waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerBlocked })
	if status.Completed != 0 || status.Failure != mediaRecoveryFailureObjectUnproven {
		t.Fatal("worker accepted or misclassified altered stage bytes", status)
	}
	if value, err := os.ReadFile(path); err != nil || string(value) != string(changed) {
		t.Fatal("failed worker changed the unproven stage", err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("failed worker changed pending rows", count)
	}
	if err = os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = worker.retry(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
}

func TestMediaRecoveryWorkerLatchesPartialFailureAndCountsOnlyAcknowledgedRows(t *testing.T) {
	f := newRecoveryFixture(t)
	ids, _, err := f.storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 {
		t.Fatal("pending fixture failed", ids, err)
	}
	execSQL(t, f.store.db, fmt.Sprintf(`CREATE TRIGGER worker_failure BEFORE DELETE ON media_pending_objects WHEN OLD.id='%s' BEGIN SELECT RAISE(ABORT,'private details'); END`, ids[1]))
	before := domainState(t, f.store)
	worker := testMediaRecoveryWorker(t, f.storage)
	status := waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerBlocked })
	if status.Completed != 1 || status.Failure != mediaRecoveryFailureOther || strings.Contains(fmt.Sprint(status), "private details") {
		t.Fatal("worker misreported a partial cleanup", status)
	}
	if scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", ids[0]) != 0 ||
		scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects WHERE id=?", ids[1]) != 1 {
		t.Fatal("worker changed the wrong journal rows")
	}
	execSQL(t, f.store.db, "DROP TRIGGER worker_failure")
	if err = worker.notify(); !errors.Is(err, errMediaRecoveryWorkerBlocked) {
		t.Fatal("ordinary notification retried a failed recovery", err)
	}
	if err = worker.retry(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
	requireDomainState(t, f.store, before)
}

func TestMediaRecoveryWorkerHasExclusiveOwnershipUntilStopped(t *testing.T) {
	f := newRecoveryFixture(t)
	unknown := filepath.Join(f.storage.path, "staging", "untracked")
	if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := testMediaRecoveryWorker(t, f.storage)
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerBlocked })
	if duplicate, err := startMediaRecoveryWorker(t.Context(), f.storage); !errors.Is(err, errMediaRecoveryWorkerActive) || duplicate != nil {
		if duplicate != nil {
			_ = duplicate.close()
		}
		t.Fatal("a second worker bypassed the blocked owner", err)
	}
	if err := f.storage.Close(); !errors.Is(err, errMediaRecoveryWorkerActive) {
		t.Fatal("storage closed while its worker was still active", err)
	}
	if value, err := os.ReadFile(unknown); err != nil || string(value) != "keep" {
		t.Fatal("duplicate worker or close changed the unknown file", string(value), err)
	}
	if err := worker.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	next, err := startMediaRecoveryWorker(t.Context(), f.storage)
	if err != nil {
		t.Fatal("stopped worker did not release its owner", err)
	}
	defer next.close()
	waitMediaRecoveryWorker(t, next, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
}

func TestMediaRecoveryWorkerRejectsConcurrentDuplicateStarts(t *testing.T) {
	f := newRecoveryFixture(t)
	type startResult struct {
		worker *mediaRecoveryWorker
		err    error
	}
	const contenders = 8
	start := make(chan struct{})
	results := make(chan startResult, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			worker, err := startMediaRecoveryWorker(t.Context(), f.storage)
			results <- startResult{worker, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var winner *mediaRecoveryWorker
	for result := range results {
		if result.err == nil && result.worker != nil && winner == nil {
			winner = result.worker
			continue
		}
		if result.worker != nil {
			_ = result.worker.close()
		}
		if !errors.Is(result.err, errMediaRecoveryWorkerActive) {
			if winner != nil {
				_ = winner.close()
			}
			t.Fatal("concurrent duplicate worker was accepted", result.err)
		}
	}
	if winner == nil {
		t.Fatal("no concurrent worker acquired ownership")
	}
	if err := winner.close(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaRecoveryWorkerCancellationWaitsForExitAndReleasesStorage(t *testing.T) {
	f := newRecoveryFixture(t)
	held, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	worker := testMediaRecoveryWorker(t, f.storage)
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerRunning })
	closed := make(chan error, 1)
	go func() { closed <- worker.close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after cancellation")
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = f.storage.validate(t.Context()); err != nil {
		t.Fatal("canceled worker retained the storage lock", err)
	}
	if status := worker.snapshot(); status.Phase != mediaRecoveryWorkerStopped {
		t.Fatal("closed worker did not report stopped", status)
	}
	if err = worker.notify(); !errors.Is(err, errMediaRecoveryWorkerClosed) {
		t.Fatal("closed worker accepted a notification", err)
	}
	if err = worker.retry(); !errors.Is(err, errMediaRecoveryWorkerClosed) {
		t.Fatal("closed worker accepted a retry", err)
	}
	if err = worker.close(); err != nil {
		t.Fatal("worker close was not idempotent", err)
	}
}

func TestMediaRecoveryWorkerCoalescesSignalsDuringAnActivePass(t *testing.T) {
	f := newRecoveryFixture(t)
	held, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	worker := testMediaRecoveryWorker(t, f.storage)
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerRunning })
	for range 20 {
		if err = worker.notify(); err != nil {
			t.Fatal(err)
		}
	}
	worker.mu.Lock()
	queued := len(worker.wake)
	worker.mu.Unlock()
	if queued != 1 {
		t.Fatal("concurrent notifications were not bounded and coalesced", queued)
	}
	if err = worker.retry(); !errors.Is(err, errMediaRecoveryWorkerBusy) {
		t.Fatal("running worker accepted a blocked-only retry", err)
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	status := waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
	worker.mu.Lock()
	queued = len(worker.wake)
	worker.mu.Unlock()
	if queued != 0 || status.Failure != mediaRecoveryFailureNone {
		t.Fatal("coalesced notification was lost or left pending", queued, status)
	}
}

func TestMediaRecoveryWorkerConcurrentSignalsAndClose(t *testing.T) {
	f := newRecoveryFixture(t)
	worker := testMediaRecoveryWorker(t, f.storage)
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 25 {
				_ = worker.notify()
				_ = worker.snapshot()
			}
		}()
	}
	group.Add(1)
	go func() { defer group.Done(); _ = worker.close() }()
	group.Wait()
	if status := worker.snapshot(); status.Phase != mediaRecoveryWorkerStopped {
		t.Fatal("worker did not stop after concurrent close", status)
	}
	if err := f.storage.validate(t.Context()); err != nil {
		t.Fatal("worker close left storage unusable", err)
	}
}

func TestMediaRecoveryWorkerClassifiesOnlySafeFailureCodes(t *testing.T) {
	for _, test := range []struct {
		err  error
		want mediaRecoveryFailure
	}{
		{errors.Join(errMediaStagingUnproven, errors.New("private name")), mediaRecoveryFailureStagingUnproven},
		{errMediaStagingLimit, mediaRecoveryFailureStagingLimit},
		{errMediaRecoveryUnproven, mediaRecoveryFailureObjectUnproven},
		{ErrUnsafePath, mediaRecoveryFailureStorage},
		{errMediaStorageClosed, mediaRecoveryFailureStorage},
		{ErrDomainState, mediaRecoveryFailureState},
		{errors.New("private database error"), mediaRecoveryFailureOther},
	} {
		if got := mediaRecoveryFailureFor(test.err); got != test.want || strings.Contains(string(got), "private") {
			t.Fatal("failure classification disclosed or changed private detail", got, test.want)
		}
	}
}

func TestMediaRecoveryWorkerSaturatesItsAcknowledgedCounter(t *testing.T) {
	f := newRecoveryFixture(t)
	held, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	worker := testMediaRecoveryWorker(t, f.storage)
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerRunning })
	worker.mu.Lock()
	worker.status.Completed = maxSafeInteger - 1
	worker.mu.Unlock()
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	status := waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerIdle })
	if status.Completed != maxSafeInteger || !status.Saturated {
		t.Fatal("worker progress counter overflowed or hid saturation", status)
	}
	if err = worker.retry(); !errors.Is(err, errMediaRecoveryWorkerBusy) {
		t.Fatal("idle worker accepted a blocked-only retry", err)
	}
}

func TestMediaRecoveryWorkerValidatesConstructionAndParentCancellation(t *testing.T) {
	var missing *mediaRecoveryWorker
	if err := missing.notify(); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil worker accepted a notification", err)
	}
	if err := missing.retry(); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil worker accepted a retry", err)
	}
	if err := missing.close(); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil worker accepted close", err)
	}
	f := newRecoveryFixture(t)
	if _, err := startMediaRecoveryWorker(nil, f.storage); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context was accepted", err)
	}
	if _, err := startMediaRecoveryWorker(t.Context(), nil); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil storage was accepted", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := startMediaRecoveryWorker(canceled, f.storage); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled parent started a worker", err)
	}
	parent, stop := context.WithCancel(t.Context())
	worker, err := startMediaRecoveryWorker(parent, f.storage)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerStopped })
	if err = worker.close(); err != nil {
		t.Fatal(err)
	}
	if err = f.storage.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = startMediaRecoveryWorker(t.Context(), f.storage); !errors.Is(err, errMediaStorageClosed) {
		t.Fatal("worker accepted closed storage", err)
	}
}
