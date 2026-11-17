//go:build linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testMediaFiles(t *testing.T) (*Store, *Auth, *MediaFiles, string) {
	t.Helper()
	s, auth, _, _, token := completedAuthFixture(t)
	files, err := s.StartMediaFiles(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle
	})
	return s, auth, files, token
}

func TestMediaFilesAutomaticallyRecoversSuccessfulAndFailedUploads(t *testing.T) {
	s, _, files, token := testMediaFiles(t)
	raw := privateUploadPNG(t, "private metadata")
	value, err := files.Upload(t.Context(), token, "managed-success-key", "image/png", raw)
	if errors.Is(err, errMediaAnonymousUnsupported) {
		t.Skip("anonymous staging or linking is unavailable on this filesystem")
	}
	if err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("successful upload was not reconciled", count)
	}
	actual, err := loadMedia(t.Context(), s.db, value.ID)
	if err != nil || actual.thumbnail == nil {
		t.Fatal("published Media or thumbnail is missing", err)
	}
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err := os.Stat(filepath.Join(files.storage.path, "objects", key)); err != nil {
			t.Fatal("recovery removed a referenced object", err)
		}
	}
	status := files.RecoveryStatus()
	if status.Phase != "idle" || status.Completed != 2 || status.Saturated || status.Failure != "" {
		t.Fatal("unexpected public recovery status", status)
	}

	execSQL(t, s.db, `CREATE TRIGGER managed_upload_failure BEFORE INSERT ON media BEGIN SELECT RAISE(ABORT,'private failure'); END`)
	if result, err := files.Upload(t.Context(), token, "managed-failure-key", "image/jpeg", privateUploadJPEG(t)); err == nil || result != (Media{}) {
		t.Fatal("failed publication returned an acknowledgement", result, err)
	}
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 4
	})
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("failed upload did not notify recovery", count)
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media"); count != 1 {
		t.Fatal("failed upload changed Media source", count)
	}
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err := os.Stat(filepath.Join(files.storage.path, "objects", key)); err != nil {
			t.Fatal("failed-attempt recovery removed a referenced object", err)
		}
	}
	execSQL(t, s.db, "DROP TRIGGER managed_upload_failure")
	if replay, err := files.Upload(t.Context(), token, "managed-success-key", "image/png", raw); err != nil || replay != value {
		t.Fatal("managed cleanup changed the original-key replay", replay, err)
	}
}

func TestMediaFilesBlockedRecoveryRetainsUploadsUntilExplicitRetry(t *testing.T) {
	s, _, files, token := testMediaFiles(t)
	unknown := filepath.Join(files.storage.path, "staging", "untracked-private-name")
	if err := os.WriteFile(unknown, []byte("leave untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.worker.notify(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerBlocked
	})
	if status := files.RecoveryStatus(); status.Phase != "blocked" || status.Failure != "staging-unproven" {
		t.Fatal("blocked status was not safely reported", status)
	}
	raw := privateUploadPNG(t, "private")
	value, err := files.Upload(t.Context(), token, "managed-blocked-key", "image/png", raw)
	if errors.Is(err, errMediaAnonymousUnsupported) {
		t.Skip("anonymous staging or linking is unavailable on this filesystem")
	}
	if err != nil {
		t.Fatal("blocked cleanup unexpectedly prevented publication", err)
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("blocked worker did not retain both pending proofs", count)
	}
	if replay, err := files.Upload(t.Context(), token, "managed-blocked-key", "image/png", raw); err != nil || replay != value {
		t.Fatal("blocked cleanup prevented original-key reconciliation", replay, err)
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "leave untouched" {
		t.Fatal("worker altered an unproven file", err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if status := files.RecoveryStatus(); status.Phase != "blocked" || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
		t.Fatal("upload notification silently retried recovery", status)
	}
	if err := files.RetryRecovery(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
	if _, err := loadMedia(t.Context(), s.db, value.ID); err != nil {
		t.Fatal("explicit cleanup removed the published Media source", err)
	}
	if err := files.RetryRecovery(); !errors.Is(err, ErrMediaRecoveryNotBlocked) {
		t.Fatal("idle recovery accepted a blocked-only retry", err)
	}
}

func TestMediaFilesPreservesUploadAuthorizationAndExactIdempotency(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	raw := privateUploadPNG(t, "private one")
	if got, err := files.Upload(t.Context(), "invalid-token", "managed-auth-key", "image/png", raw); !errors.Is(err, ErrAuthentication) || got != (Media{}) {
		t.Fatal("managed upload bypassed session validation", got, err)
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media"); count != 0 {
		t.Fatal("unauthorized upload published a source", count)
	}
	value, err := files.Upload(t.Context(), token, "managed-exact-key", "image/png", raw)
	if errors.Is(err, errMediaAnonymousUnsupported) {
		t.Skip("anonymous staging or linking is unavailable on this filesystem")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got, err := files.Upload(t.Context(), token, "managed-exact-key", "image/png", privateUploadPNG(t, "private two")); !errors.Is(err, ErrIdempotencyConflict) || got != (Media{}) {
		t.Fatal("managed upload accepted changed original bytes with the same key", got, err)
	}
	if replay, err := files.Upload(t.Context(), token, "managed-exact-key", "image/png", raw); err != nil || replay != value {
		t.Fatal("managed upload lost the exact original-key response", replay, err)
	}
	if _, err := auth.Logout(t.Context(), token, "managed-logout-key"); err != nil {
		t.Fatal(err)
	}
	if got, err := files.Upload(t.Context(), token, "managed-revoked-key", "image/png", raw); !errors.Is(err, ErrAuthentication) || got != (Media{}) {
		t.Fatal("managed upload accepted a revoked session", got, err)
	}
}

func TestMediaFilesCanceledAfterJournalStillNotifiesRecovery(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	clock := auth.now
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	auth.now = func() time.Time {
		calls++
		if calls == 7 {
			cancel()
		}
		return clock()
	}
	result, err := files.Upload(ctx, token, "managed-canceled-key", "image/png", privateUploadPNG(t, "private"))
	if errors.Is(err, errMediaAnonymousUnsupported) {
		t.Skip("anonymous staging or linking is unavailable on this filesystem")
	}
	if calls < 7 || !errors.Is(err, context.Canceled) || result != (Media{}) {
		t.Fatal("canceled publication returned an acknowledgement", calls, result, err)
	}
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool {
		return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
	})
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("canceled request did not notify recovery", count)
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media"); count != 0 {
		t.Fatal("canceled upload committed a Media source", count)
	}
	entries, err := os.ReadDir(filepath.Join(files.storage.path, "objects"))
	if err != nil || len(entries) != 1 || entries[0].Name() != mediaOwnerName {
		t.Fatal("canceled upload retained an unreferenced object", entries, err)
	}
}

func TestMediaFilesSingleOwnerAndReopen(t *testing.T) {
	s, auth, files, _ := testMediaFiles(t)
	if duplicate, err := s.StartMediaFiles(t.Context(), auth); !errors.Is(err, ErrMediaFilesActive) || duplicate != nil {
		if duplicate != nil {
			_ = duplicate.Close()
		}
		t.Fatal("duplicate managed service was accepted", err)
	}
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	if err := files.Close(); err != nil {
		t.Fatal("service close was not idempotent", err)
	}
	if status := files.RecoveryStatus(); status.Phase != "stopped" {
		t.Fatal("closed service reported an active worker", status)
	}
	if _, err := files.Upload(t.Context(), "token", "request-key", "image/png", []byte{1}); !errors.Is(err, ErrMediaFilesClosed) {
		t.Fatal("closed service accepted upload", err)
	}
	if err := files.RetryRecovery(); !errors.Is(err, ErrMediaFilesClosed) {
		t.Fatal("closed service accepted a retry", err)
	}
	reopened, err := s.StartMediaFiles(t.Context(), auth)
	if err != nil {
		t.Fatal("closed service did not release storage", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaFilesConcurrentStartsHaveOneOwner(t *testing.T) {
	s, auth, _, _, _ := completedAuthFixture(t)
	type result struct {
		files *MediaFiles
		err   error
	}
	const count = 8
	results := make(chan result, count)
	start := make(chan struct{})
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			files, err := s.StartMediaFiles(t.Context(), auth)
			results <- result{files, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var winner *MediaFiles
	for value := range results {
		if value.err == nil && value.files != nil && winner == nil {
			winner = value.files
			continue
		}
		if value.files != nil {
			_ = value.files.Close()
		}
		if !errors.Is(value.err, ErrMediaFilesActive) {
			t.Fatal("concurrent start did not report the active owner", value.err)
		}
	}
	if winner == nil {
		t.Fatal("no service acquired the storage owner")
	}
	if err := winner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCloseWaitsForManagedUploadAndStopsRecovery(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	path := s.path
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	raw := privateUploadPNG(t, "private")
	type outcome struct {
		value Media
		err   error
	}
	upload := make(chan outcome, 1)
	go func() {
		value, err := files.Upload(t.Context(), token, "managed-shutdown-key", "image/png", raw)
		upload <- outcome{value, err}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
waitForUpload:
	for {
		if !files.storage.mu.TryLock() {
			break waitForUpload
		}
		files.storage.mu.Unlock()
		select {
		case value := <-upload:
			t.Fatalf("upload returned before acquiring storage: %v", value.err)
		case <-deadline.C:
			t.Fatal("upload did not acquire storage")
		case <-ticker.C:
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatal("Store closed while upload was still using it", err)
	case <-time.After(100 * time.Millisecond):
	}
	for {
		s.mediaMu.Lock()
		closing := s.closing
		s.mediaMu.Unlock()
		if closing {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("Store did not enter its closing state")
		case <-ticker.C:
		}
	}
	if _, err = s.StartMediaFiles(t.Context(), auth); !errors.Is(err, ErrMediaFilesClosed) {
		t.Fatal("closing Store accepted a new Media service", err)
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	var result outcome
	select {
	case result = <-upload:
	case <-time.After(20 * time.Second):
		t.Fatal("upload did not finish after database release")
	}
	if errors.Is(result.err, errMediaAnonymousUnsupported) {
		t.Skip("anonymous staging or linking is unavailable on this filesystem")
	}
	if result.err != nil {
		t.Fatal("Store close interrupted an admitted upload", result.err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Store close did not wait for worker exit")
	}
	if status := files.RecoveryStatus(); status.Phase != "stopped" {
		t.Fatal("Store close did not stop the worker", status)
	}
	if _, err := s.StartMediaFiles(t.Context(), auth); !errors.Is(err, ErrMediaFilesClosed) {
		t.Fatal("closed Store accepted a new service", err)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal("Store close retained the database lock", err)
	}
	defer reopened.Close()
	freshAuth, err := reopened.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := reopened.StartMediaFiles(t.Context(), freshAuth)
	if err != nil {
		t.Fatal("Store close retained the media lock", err)
	}
	defer fresh.Close()
	waitMediaRecoveryWorker(t, fresh.worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerIdle })
	if count := scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("reopened service did not reconcile a shutdown-interrupted attempt", count)
	}
	actual, err := loadMedia(t.Context(), reopened.db, result.value.ID)
	if err != nil || actual.thumbnail == nil {
		t.Fatal("shutdown lost the published Media", err)
	}
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err := os.Stat(filepath.Join(fresh.storage.path, "objects", key)); err != nil {
			t.Fatal("shutdown recovery lost a referenced file", err)
		}
	}
}

func TestMediaFilesFailedStartupDoesNotReserveTheOwner(t *testing.T) {
	s, auth, _, _, _ := completedAuthFixture(t)
	target := privateTempDir(t)
	path := s.path + ".media"
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if files, err := s.StartMediaFiles(t.Context(), auth); !errors.Is(err, ErrUnsafePath) || files != nil {
		if files != nil {
			_ = files.Close()
		}
		t.Fatal("managed service accepted a symlinked root", err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatal("failed startup changed the foreign directory", entries, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	files, err := s.StartMediaFiles(t.Context(), auth)
	if err != nil {
		t.Fatal("failed startup retained the managed owner", err)
	}
	if err = files.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaFilesValidateStartupAndContextLifetime(t *testing.T) {
	s, auth, _, _, _ := completedAuthFixture(t)
	var absent *Store
	if _, err := absent.StartMediaFiles(t.Context(), auth); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil Store accepted", err)
	}
	if _, err := s.StartMediaFiles(nil, auth); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context accepted", err)
	}
	if _, err := s.StartMediaFiles(t.Context(), nil); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil auth accepted", err)
	}
	s.offlineAuth = true
	if _, err := s.StartMediaFiles(t.Context(), auth); !errors.Is(err, ErrDomainInput) {
		t.Fatal("offline recovery Store started background Media work", err)
	}
	s.offlineAuth = false
	var noFiles *MediaFiles
	if status := noFiles.RecoveryStatus(); status.Phase != "stopped" {
		t.Fatal("nil service did not report stopped", status)
	}
	if _, err := noFiles.Upload(t.Context(), "token", "request-key", "image/png", []byte{1}); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil service accepted an upload", err)
	}
	if err := noFiles.RetryRecovery(); !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil service accepted a retry", err)
	}
	if err := noFiles.Close(); err != nil {
		t.Fatal("nil service close failed", err)
	}
	_, otherAuth, _, _, _ := completedAuthFixture(t)
	if _, err := s.StartMediaFiles(t.Context(), otherAuth); !errors.Is(err, ErrDomainInput) {
		t.Fatal("auth from another Store accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.StartMediaFiles(ctx, auth); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled startup accepted", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	files, err := s.StartMediaFiles(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	cancel()
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerIdle })
	if status := files.RecoveryStatus(); status.Phase != "idle" {
		t.Fatal("startup context cancellation stopped an owned service", status)
	}
	if err := files.RetryRecovery(); !errors.Is(err, ErrMediaRecoveryNotBlocked) {
		t.Fatal("idle retry was not rejected", err)
	}
}
