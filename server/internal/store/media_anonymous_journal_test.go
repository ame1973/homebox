//go:build linux

package store

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func anonymousJournalStages(t *testing.T, dir string) []*mediaStage {
	t.Helper()
	stages := []*mediaStage{anonymousTestStage(t, dir, 128), anonymousTestStage(t, dir, 128)}
	for index, value := range []string{"sanitized original", "sanitized thumbnail"} {
		if _, err := stages[index].Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
		if _, err := stages[index].seal(); err != nil {
			t.Fatal(err)
		}
	}
	return stages
}

func TestAnonymousMediaJournalCommitsExactUnlinkedStages(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	stages := anonymousJournalStages(t, filepath.Join(storage.path, "staging"))
	storage.mu.Lock()
	pending, err := library.journalMediaObjects(t.Context(), token, stages, storage.objects)
	storage.mu.Unlock()
	if err != nil || len(pending) != 2 {
		t.Fatal("anonymous stage journal failed", pending, err)
	}
	for index, row := range pending {
		actual, err := loadMediaPendingRow(t.Context(), s.db, row.ID)
		if err != nil || actual != row || row.StageName != stages[index].name || row.StageInode != int64(stages[index].inode) {
			t.Fatal("journal did not preserve the actual anonymous identity", row, actual, err)
		}
		if _, err = os.Lstat(filepath.Join(storage.path, "staging", row.StageName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("journal published a stage name", err)
		}
		if err = stages[index].preserve(); err != nil {
			t.Fatal("could not close the still-anonymous stage", err)
		}
	}
	before := domainState(t, s)
	completed, more, err := storage.recoverPendingMediaBatch(t.Context(), 10)
	if err != nil || completed != 2 || more {
		t.Fatal("absent anonymous stages were not recoverable", completed, more, err)
	}
	requireDomainState(t, s, before)
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("recovery retained absent anonymous stages", count)
	}
}

func TestAnonymousMediaJournalNeverClaimsAnExistingUnreferencedObject(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	stages := anonymousJournalStages(t, filepath.Join(storage.path, "staging"))
	storage.mu.Lock()
	pending, err := library.journalMediaObjects(t.Context(), token, stages, storage.objects)
	storage.mu.Unlock()
	if err != nil || len(pending) != 2 {
		t.Fatal("anonymous journal failed", pending, err)
	}
	path := filepath.Join(storage.path, "objects", "sha256-"+pending[0].SHA256)
	if err = os.WriteFile(path, []byte("sanitized original"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if err = stage.preserve(); err != nil {
			t.Fatal(err)
		}
	}
	before := domainState(t, s)
	if _, err = storage.recoverPendingMedia(t.Context(), pending[0].ID); !errors.Is(err, errMediaRecoveryUnproven) {
		t.Fatal("missing stage claimed an existing unreferenced object", err)
	}
	requireDomainState(t, s, before)
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("uncertain object caused journal deletion", count)
	}
	if value, err := os.ReadFile(path); err != nil || string(value) != "sanitized original" {
		t.Fatal("recovery changed the unproven object", string(value), err)
	}
}

func TestAnonymousMediaJournalRejectsNameCollisionWithoutRemovingIt(t *testing.T) {
	_, _, library, token, staging, objectPath := privateUploadFixture(t)
	stages := anonymousJournalStages(t, staging)
	objects, err := openPrivateMediaDirectory(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	collision := filepath.Join(staging, stages[0].name)
	if err = os.WriteFile(collision, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows, err := library.journalMediaObjects(t.Context(), token, stages, objects); !errors.Is(err, errMediaStageChanged) || len(rows) != 0 {
		t.Fatal("occupied name was journaled", rows, err)
	}
	if err = stages[0].discard(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("anonymous close did not report the occupied name", err)
	}
	value, err := os.ReadFile(collision)
	if err != nil || string(value) != "unrelated" {
		t.Fatal("journal cleanup removed an unrelated name", string(value), err)
	}
}

func TestAnonymousMediaJournalFailureLeavesNoNamedStages(t *testing.T) {
	s, _, library, token, staging, objectPath := privateUploadFixture(t)
	stages := anonymousJournalStages(t, staging)
	objects, err := openPrivateMediaDirectory(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	execSQL(t, s.db, `CREATE TABLE anonymous_journal_fault(value TEXT REFERENCES media(id) DEFERRABLE INITIALLY DEFERRED)`)
	execSQL(t, s.db, `CREATE TRIGGER fail_anonymous_journal AFTER INSERT ON media_pending_objects BEGIN INSERT INTO anonymous_journal_fault VALUES('missing'); END`)
	rows, err := library.journalMediaObjects(t.Context(), token, stages, objects)
	if err == nil || len(rows) != 2 {
		t.Fatal("deferred COMMIT failure did not return an uncertain pair", rows, err)
	}
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("the deferred constraint unexpectedly committed", count)
	}
	for _, stage := range stages {
		if err = stage.preserve(); err != nil {
			t.Fatal(err)
		}
	}
	if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
		t.Fatal("failed anonymous journal left named stages", entries, err)
	}
	if entries, err := os.ReadDir(objectPath); err != nil || len(entries) != 0 {
		t.Fatal("failed anonymous journal promoted an object", entries, err)
	}
}

func TestAnonymousMediaJournalRollsBackLateSQLChanges(t *testing.T) {
	for _, test := range []struct{ name, trigger string }{
		{"ignored insert", `CREATE TRIGGER anonymous_failure BEFORE INSERT ON media_pending_objects BEGIN SELECT RAISE(IGNORE); END`},
		{"revoked session", `CREATE TRIGGER anonymous_failure AFTER INSERT ON media_pending_objects BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END`},
		{"changed epoch", `CREATE TRIGGER anonymous_failure AFTER INSERT ON media_pending_objects BEGIN UPDATE sync_state SET dataset_epoch=lower(hex(randomblob(16))) WHERE singleton=1; END`},
		{"removed after activity", `CREATE TRIGGER anonymous_failure AFTER UPDATE ON sessions BEGIN DELETE FROM media_pending_objects; END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			s, _, library, token, staging, objectPath := privateUploadFixture(t)
			stages := anonymousJournalStages(t, staging)
			objects, err := openPrivateMediaDirectory(objectPath)
			if err != nil {
				t.Fatal(err)
			}
			defer objects.Close()
			before := domainState(t, s)
			execSQL(t, s.db, test.trigger)
			if pending, err := library.journalMediaObjects(t.Context(), token, stages, objects); err == nil || pending != nil {
				t.Fatal("late SQL change committed the anonymous journal", pending, err)
			}
			requireDomainState(t, s, before)
			if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
				t.Fatal("failed journal retained partial rows", count)
			}
			if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
				t.Fatal("failed journal published a stage name", entries, err)
			}
		})
	}
}

func TestAnonymousMediaJournalRejectsLateNameCollision(t *testing.T) {
	s, auth, library, token, staging, objectPath := privateUploadFixture(t)
	stages := anonymousJournalStages(t, staging)
	objects, err := openPrivateMediaDirectory(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	path := filepath.Join(staging, stages[0].name)
	clock := auth.now
	calls := 0
	var createErr error
	auth.now = func() time.Time {
		calls++
		if calls == 3 {
			createErr = os.WriteFile(path, []byte("unrelated"), 0o600)
		}
		return clock()
	}
	before := domainState(t, s)
	pending, err := library.journalMediaObjects(t.Context(), token, stages, objects)
	if calls < 3 || createErr != nil || !errors.Is(err, errMediaStageChanged) || pending != nil {
		t.Fatal("late name collision was journaled", calls, createErr, pending, err)
	}
	requireDomainState(t, s, before)
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("late collision retained journal rows", count)
	}
	if err = stages[0].discard(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("anonymous close did not report the late collision", err)
	}
	if value, err := os.ReadFile(path); err != nil || string(value) != "unrelated" {
		t.Fatal("late collision cleanup changed the unrelated file", string(value), err)
	}
}

func TestAnonymousMediaJournalRecoversAfterProcessExit(t *testing.T) {
	const databaseEnv = "HOMEBOX_ANONYMOUS_JOURNAL_EXIT_DB"
	const clockEnv = "HOMEBOX_ANONYMOUS_JOURNAL_EXIT_CLOCK"
	if path := os.Getenv(databaseEnv); path != "" {
		ctx := context.Background()
		token, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(ctx, Config{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		auth, err := s.NewAuth(AuthConfig{})
		if err != nil {
			t.Fatal(err)
		}
		now, err := time.Parse(time.RFC3339Nano, os.Getenv(clockEnv))
		if err != nil {
			t.Fatal(err)
		}
		auth.now = func() time.Time { return now }
		library, err := NewMediaLibrary(auth)
		if err != nil {
			t.Fatal(err)
		}
		storage, err := s.openMediaStorage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		stages := make([]*mediaStage, 2)
		for index, text := range []string{"original", "thumbnail"} {
			stages[index], err = newAnonymousMediaStage(ctx, filepath.Join(storage.path, "staging"), 128)
			if errors.Is(err, errMediaAnonymousUnsupported) {
				os.Exit(77)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = stages[index].Write([]byte(text)); err != nil {
				t.Fatal(err)
			}
			if _, err = stages[index].seal(); err != nil {
				t.Fatal(err)
			}
		}
		storage.mu.Lock()
		if pending, err := library.journalMediaObjects(ctx, string(token), stages, storage.objects); err != nil || len(pending) != 2 {
			t.Fatal("anonymous journal did not commit", pending, err)
		}
		// Simulate exit while both journaled inodes are still unnamed and open.
		os.Exit(0)
	}
	s, _, now, _, token := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	path := s.path
	if err := errors.Join(storage.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAnonymousMediaJournalRecoversAfterProcessExit$")
	cmd.Env = append(os.Environ(), databaseEnv+"="+path, clockEnv+"="+now.Format(time.RFC3339Nano))
	cmd.Stdin = strings.NewReader(token)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 77 {
		t.Skip("anonymous temporary files are unavailable on this test filesystem")
	}
	if err != nil {
		t.Fatalf("anonymous journal child failed: %v %s", err, output)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	owner, err := reopened.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ids, next, err := owner.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 || next != "" {
		t.Fatal("producer exit lost committed journal rows", ids, next, err)
	}
	entries, err := os.ReadDir(filepath.Join(owner.path, "staging"))
	if err != nil || len(entries) != 1 || entries[0].Name() != mediaOwnerName {
		t.Fatal("producer exit left a named stage", entries, err)
	}
	before := domainState(t, reopened)
	completed, more, err := owner.recoverPendingMediaBatch(t.Context(), 10)
	if err != nil || completed != 2 || more {
		t.Fatal("restart did not recover absent anonymous stages", completed, more, err)
	}
	requireDomainState(t, reopened, before)
}
