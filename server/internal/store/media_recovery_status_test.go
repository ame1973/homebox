//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMediaRecoveryStatusAuthorizesLiveAndUnavailableSnapshots(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	idle, err := auth.ReadMediaRecoveryStatus(t.Context(), token, files)
	if err != nil || idle.Phase != "idle" || idle.Completed != 0 || idle.Saturated || idle.Failure != "" {
		t.Fatal("authorized idle status was not returned", idle, err)
	}
	unavailable, err := auth.ReadMediaRecoveryStatus(t.Context(), token, nil)
	if err != nil || unavailable.Phase != "unavailable" || unavailable.Failure != "storage-unavailable" || unavailable.Completed != 0 || unavailable.Saturated {
		t.Fatal("unavailable storage was not reported safely", unavailable, err)
	}
	private := filepath.Join(files.storage.path, "staging", "private-unknown-name")
	if err := os.WriteFile(private, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.worker.notify(); err != nil {
		t.Fatal(err)
	}
	waitMediaRecoveryWorker(t, files.worker, func(status mediaRecoveryWorkerStatus) bool { return status.Phase == mediaRecoveryWorkerBlocked })
	blocked, err := auth.ReadMediaRecoveryStatus(t.Context(), token, files)
	if err != nil || blocked.Phase != "blocked" || blocked.Failure != "staging-unproven" || strings.Contains(fmt.Sprint(blocked), "private-unknown-name") {
		t.Fatal("blocked status leaked or changed its safe category", blocked, err)
	}
	for _, candidate := range []*MediaFiles{files, nil} {
		status, err := auth.ReadMediaRecoveryStatus(t.Context(), "invalid-token", candidate)
		if !errors.Is(err, ErrAuthentication) || status != (MediaRecoveryStatus{}) {
			t.Fatal("unauthorized status disclosed a snapshot", status, err)
		}
	}
	for _, failure := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored activity", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-media-status' WHERE singleton=1", ErrAuthentication},
		{"mandatory password", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
	} {
		t.Run(failure.name, func(t *testing.T) {
			execSQL(t, s.db, "CREATE TRIGGER late_media_status_session "+failure.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+failure.statement+"; END")
			defer execSQL(t, s.db, "DROP TRIGGER late_media_status_session")
			before := domainState(t, s)
			for _, candidate := range []*MediaFiles{files, nil} {
				status, err := auth.ReadMediaRecoveryStatus(t.Context(), token, candidate)
				if !errors.Is(err, failure.want) || status != (MediaRecoveryStatus{}) {
					t.Fatal("status ignored authorization change during session activity", status, err)
				}
				requireDomainState(t, s, before)
			}
		})
	}
	if data, err := os.ReadFile(private); err != nil || string(data) != "retain" {
		t.Fatal("status or blocked recovery changed an unknown stage", err)
	}
}

func TestMediaRecoveryStatusRejectsMismatchedOwnersAndIncompleteSessions(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	_, foreign, _, _, foreignToken := completedAuthFixture(t)
	if value, err := foreign.ReadMediaRecoveryStatus(t.Context(), foreignToken, files); !errors.Is(err, ErrDomainInput) || value != (MediaRecoveryStatus{}) {
		t.Fatal("status accepted a different Store", value, err)
	}
	other, err := s.NewAuth(AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := other.ReadMediaRecoveryStatus(t.Context(), token, files); !errors.Is(err, ErrDomainInput) || value != (MediaRecoveryStatus{}) {
		t.Fatal("status accepted a differently configured authentication service", value, err)
	}
	if value, err := auth.ReadMediaRecoveryStatus(nil, token, files); !errors.Is(err, ErrDomainInput) || value != (MediaRecoveryStatus{}) {
		t.Fatal("status accepted a nil context", value, err)
	}
	var absent *Auth
	if value, err := absent.ReadMediaRecoveryStatus(t.Context(), token, nil); !errors.Is(err, ErrDomainInput) || value != (MediaRecoveryStatus{}) {
		t.Fatal("status accepted nil authentication", value, err)
	}
	for _, broken := range []*MediaFiles{{}, {store: s}, {store: s, library: &MediaLibrary{auth: auth}}} {
		if value, err := auth.ReadMediaRecoveryStatus(t.Context(), token, broken); !errors.Is(err, ErrDomainInput) || value != (MediaRecoveryStatus{}) {
			t.Fatal("status accepted an incomplete Media owner", value, err)
		}
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	for _, candidate := range []*MediaFiles{files, nil} {
		status, err := auth.ReadMediaRecoveryStatus(t.Context(), token, candidate)
		if !errors.Is(err, ErrPasswordChangeRequired) || status != (MediaRecoveryStatus{}) {
			t.Fatal("incomplete session read status", status, err)
		}
	}
}

func TestMediaRecoveryStatusLetsStoreCloseAfterAnAdmittedRead(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	type result struct {
		status MediaRecoveryStatus
		err    error
	}
	reading := make(chan result, 1)
	go func() {
		status, err := auth.ReadMediaRecoveryStatus(t.Context(), token, files)
		reading <- result{status, err}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !files.mu.TryLock() {
			break
		}
		files.mu.Unlock()
		select {
		case value := <-reading:
			t.Fatal("status read returned before acquiring its lifetime lock", value)
		case <-deadline.C:
			t.Fatal("status read did not acquire its lifetime lock")
		case <-ticker.C:
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatal("Store closed while status was awaiting its transaction", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = held.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-reading:
		if value.err != nil || value.status.Phase != "idle" {
			t.Fatal("admitted status read was interrupted", value)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("status read deadlocked during Store close")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Store did not close after the status read")
	}
}

func TestMediaRecoveryStatusReportsAClosedOwnerOnlyToAnAuthorizedSession(t *testing.T) {
	_, auth, files, token := testMediaFiles(t)
	if err := files.Close(); err != nil {
		t.Fatal(err)
	}
	status, err := auth.ReadMediaRecoveryStatus(t.Context(), token, files)
	if err != nil || status.Phase != "stopped" || status.Completed != 0 || status.Saturated || status.Failure != "" {
		t.Fatal("closed owner did not report a stopped worker", status, err)
	}
	if value, err := auth.ReadMediaRecoveryStatus(t.Context(), "invalid-token", files); !errors.Is(err, ErrAuthentication) || value != (MediaRecoveryStatus{}) {
		t.Fatal("closed owner disclosed status to an unauthorized session", value, err)
	}
}

func TestMediaRecoveryStatusHonorsCancellation(t *testing.T) {
	_, auth, files, token := testMediaFiles(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := auth.ReadMediaRecoveryStatus(ctx, token, files); !errors.Is(err, context.Canceled) || value != (MediaRecoveryStatus{}) {
		t.Fatal("canceled status returned data", value, err)
	}
}

func TestMediaRecoveryStatusCancelsWhileAwaitingTheDatabase(t *testing.T) {
	s, auth, files, token := testMediaFiles(t)
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		value, err := auth.ReadMediaRecoveryStatus(ctx, token, files)
		if value != (MediaRecoveryStatus{}) {
			result <- fmt.Errorf("canceled read returned status: %v", value)
			return
		}
		result <- err
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !files.mu.TryLock() {
			break
		}
		files.mu.Unlock()
		select {
		case err := <-result:
			t.Fatal("status returned before awaiting the database", err)
		case <-deadline.C:
			t.Fatal("status did not acquire its lifetime lock")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("canceled database wait did not return cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled status remained blocked on the database")
	}
	if !files.mu.TryLock() {
		t.Fatal("canceled status retained the lifetime lock")
	}
	files.mu.Unlock()
}
