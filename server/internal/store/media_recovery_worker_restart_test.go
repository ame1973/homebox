//go:build linux

package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMediaRecoveryWorkerReconcilesAfterRealProducerExit(t *testing.T) {
	for _, scenario := range []string{"before source commit", "after source commit"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, now, _, token := completedAuthFixture(t)
			owner := testMediaStorage(t, s)
			path := s.path
			if err := errors.Join(owner.Close(), s.Close()); err != nil {
				t.Fatal(err)
			}
			var cmd *exec.Cmd
			if scenario == "before source commit" {
				cmd = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStagedMediaUploadRecoversAnonymousProducerExits$")
				cmd.Env = append(os.Environ(), "HOMEBOX_ANONYMOUS_UPLOAD_EXIT_DB="+path,
					"HOMEBOX_ANONYMOUS_UPLOAD_EXIT_CLOCK="+now.Format(time.RFC3339Nano), "HOMEBOX_ANONYMOUS_UPLOAD_EXIT_CHECKPOINT="+strconv.Itoa(7))
			} else {
				cmd = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPublishedMediaRecoversAfterProducerExitAndPartialStageCleanup$")
				cmd.Env = append(os.Environ(), "HOMEBOX_PUBLISHED_MEDIA_EXIT_TEST_DB="+path,
					"HOMEBOX_PUBLISHED_MEDIA_EXIT_TEST_CLOCK="+now.Format(time.RFC3339Nano))
			}
			cmd.Stdin = strings.NewReader(token)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("anonymous staging or linking is unavailable on this filesystem")
			}
			if err != nil {
				t.Fatalf("producer process failed: %v %s", err, output)
			}
			reopened, err := Open(t.Context(), Config{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			storage, err := reopened.openMediaStorage(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			if count := scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
				t.Fatal("producer exit did not retain the pending pair", count)
			}
			var referenced []string
			if scenario == "after source commit" {
				id := strings.TrimSpace(string(output))
				actual, err := loadMedia(t.Context(), reopened.db, id)
				if err != nil || actual.thumbnail == nil {
					t.Fatal("producer did not commit its Media source", err)
				}
				referenced = []string{actual.storageKey, "sha256-" + actual.thumbnail.SHA256}
			} else if count := scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media"); count != 0 {
				t.Fatal("interrupted source transaction unexpectedly committed", count)
			}
			before := domainState(t, reopened)
			worker, err := startMediaRecoveryWorker(t.Context(), storage)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.close()
			waitMediaRecoveryWorker(t, worker, func(status mediaRecoveryWorkerStatus) bool {
				return status.Phase == mediaRecoveryWorkerIdle && status.Completed == 2
			})
			if count := scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
				t.Fatal("worker did not reconcile the producer's journal", count)
			}
			requireDomainState(t, reopened, before)
			for _, key := range referenced {
				if _, err = os.Stat(filepath.Join(storage.path, "objects", key)); err != nil {
					t.Fatal("worker removed a committed Media object", err)
				}
			}
			if scenario == "before source commit" {
				entries, err := os.ReadDir(filepath.Join(storage.path, "objects"))
				if err != nil || len(entries) != 1 || entries[0].Name() != mediaOwnerName {
					t.Fatal("worker retained an unreferenced promoted object", entries, err)
				}
			}
		})
	}
}
