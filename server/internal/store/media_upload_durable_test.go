//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishedMediaRecoversAfterProducerExitAndPartialStageCleanup(t *testing.T) {
	const databaseEnv = "HOMEBOX_PUBLISHED_MEDIA_EXIT_TEST_DB"
	const clockEnv = "HOMEBOX_PUBLISHED_MEDIA_EXIT_TEST_CLOCK"
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
		value, err := storage.upload(ctx, library, string(token), "published-exit-key", "image/png", privateUploadPNG(t, "private"))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, value.ID)
		// Exit without closing storage; the next process must reconcile both rows.
		os.Exit(0)
	}

	s, _, now, _, token := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	path := s.path
	if err := errors.Join(storage.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPublishedMediaRecoversAfterProducerExitAndPartialStageCleanup$")
	cmd.Env = append(os.Environ(), databaseEnv+"="+path, clockEnv+"="+now.Format(time.RFC3339Nano))
	cmd.Stdin = strings.NewReader(token)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("published producer failed: %v %s", err, output)
	}
	id := strings.TrimSpace(string(output))
	if !validDomainID(id) || strings.ContainsAny(id, "\r\n") {
		t.Fatal("producer did not return one valid Media identity")
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
		t.Fatal("producer exit lost recoverable rows", ids, next, err)
	}
	value, err := loadMedia(t.Context(), reopened.db, id)
	if err != nil || value.thumbnail == nil {
		t.Fatal("published source did not survive producer exit", err)
	}
	first, err := loadMediaPendingRow(t.Context(), reopened.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	// Simulate exit after the first stage unlink but before its journal deletion.
	if err = os.Remove(filepath.Join(owner.path, "staging", first.StageName)); err != nil {
		t.Fatal(err)
	}
	if err = syncRecoveryDirectory(owner.staging); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, reopened)
	for index, pendingID := range ids {
		result, err := owner.recoverPendingMedia(t.Context(), pendingID)
		if err != nil || !result.Referenced || result.ObjectRemoved || result.StageRemoved != (index == 1) {
			t.Fatal("restart did not safely reconcile the committed source", result, err)
		}
	}
	requireDomainState(t, reopened, before)
	if scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("restart cleanup retained completed rows")
	}
	for _, key := range []string{value.storageKey, "sha256-" + value.thumbnail.SHA256} {
		if _, err = os.Stat(filepath.Join(owner.path, "objects", key)); err != nil {
			t.Fatal("restart cleanup lost a committed object", err)
		}
	}
}

func TestPublishedMediaRollsBackIfLateWritesRemoveRecoveryProof(t *testing.T) {
	for _, trigger := range []struct{ name, statement string }{
		{"audit", `CREATE TRIGGER corrupt_pending AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN DELETE FROM media_pending_objects WHERE id=(SELECT MIN(id) FROM media_pending_objects); END`},
		{"session", `CREATE TRIGGER corrupt_pending AFTER UPDATE ON sessions WHEN EXISTS(SELECT 1 FROM mutation_events WHERE operation='post_media') BEGIN DELETE FROM media_pending_objects WHERE id=(SELECT MIN(id) FROM media_pending_objects); END`},
	} {
		t.Run(trigger.name, func(t *testing.T) {
			t.Parallel()
			s, _, library, token, staging, objects := privateUploadFixture(t)
			execSQL(t, s.db, trigger.statement)
			before := domainState(t, s)
			if got, err := library.uploadStagedMedia(t.Context(), token, "late-recovery-proof", "image/png", privateUploadPNG(t, "private"), staging, objects); err == nil || got != (Media{}) {
				t.Fatal("publication succeeded after a late journal deletion", got, err)
			}
			requireDomainState(t, s, before)
			requirePendingUploadStages(t, s, staging, 2)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM media") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
				t.Fatal("late proof loss was not rolled back with publication")
			}
		})
	}
}

func TestPublishedMediaKeepsRecoveryProofUntilExplicitCleanup(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	raw := privateUploadPNG(t, "private recovery source")
	first, err := storage.upload(t.Context(), library, token, "committed-recovery-proof", "image/png", raw)
	if err != nil {
		t.Fatal("source publication failed", err)
	}
	ids, next, err := storage.pendingMediaRecoveryPage(t.Context(), "", 10)
	if err != nil || len(ids) != 2 || next != "" {
		t.Fatalf("committed source lost its two recoverable cleanup proofs: ids=%v next=%q err=%v", ids, next, err)
	}
	requirePendingUploadStages(t, s, filepath.Join(storage.path, "staging"), 2)
	before := domainState(t, s)
	for _, id := range ids {
		result, err := storage.recoverPendingMedia(t.Context(), id)
		if err != nil || !result.Referenced || !result.StageRemoved || result.ObjectRemoved {
			t.Fatal("explicit cleanup did not preserve published objects", result, err)
		}
	}
	requireDomainState(t, s, before)
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("completed cleanup retained journal rows", count)
	}
	actual, err := loadMedia(t.Context(), s.db, first.ID)
	if err != nil || actual.thumbnail == nil {
		t.Fatal("cleanup changed published media", err)
	}
	for _, key := range []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256} {
		if _, err = os.Stat(filepath.Join(storage.path, "objects", key)); err != nil {
			t.Fatal("cleanup removed a published object", err)
		}
	}
	replay, err := storage.upload(t.Context(), library, token, "committed-recovery-proof", "image/png", raw)
	if err != nil || replay != first {
		t.Fatal("explicit original-key replay changed after cleanup", replay, err)
	}
}
