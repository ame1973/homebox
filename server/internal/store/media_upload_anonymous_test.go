//go:build linux

package store

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStagedMediaUploadRejectsReplacedStagingDirectory(t *testing.T) {
	for _, changeAt := range []int{2, 6, 8, 9} {
		t.Run("clock-"+strconv.Itoa(changeAt), func(t *testing.T) {
			s, auth, library, token, staging, objects := privateUploadFixture(t)
			clock := auth.now
			calls := 0
			moved := staging + "-moved"
			var swapErr error
			auth.now = func() time.Time {
				calls++
				if calls == changeAt {
					swapErr = os.Rename(staging, moved)
					if swapErr == nil {
						swapErr = os.Mkdir(staging, 0o700)
					}
				}
				return clock()
			}
			before := domainState(t, s)
			result, err := library.uploadStagedMedia(t.Context(), token, "renamed-anonymous-staging", "image/png", privateUploadPNG(t, "private"), staging, objects)
			if calls < changeAt || swapErr != nil || !errors.Is(err, ErrUnsafePath) || result != (Media{}) {
				t.Fatal("a substituted staging directory was accepted", calls, swapErr, result, err)
			}
			requireDomainState(t, s, before)
			present := 0
			if changeAt >= 8 {
				present = 2
			}
			requirePendingUploadStageState(t, s, moved, 2, present)
			if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
				t.Fatal("upload wrote into the replacement directory", entries, err)
			}
			if entries, err := os.ReadDir(objects); err != nil || changeAt < 8 && len(entries) != 0 || changeAt >= 8 && len(entries) == 0 {
				t.Fatal("unexpected object promotion after directory substitution", entries, err)
			}
		})
	}
}

func TestStagedMediaUploadRecoversAnonymousProducerExits(t *testing.T) {
	const databaseEnv = "HOMEBOX_ANONYMOUS_UPLOAD_EXIT_DB"
	const clockEnv = "HOMEBOX_ANONYMOUS_UPLOAD_EXIT_CLOCK"
	const checkpointEnv = "HOMEBOX_ANONYMOUS_UPLOAD_EXIT_CHECKPOINT"
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
		checkpoint, err := strconv.Atoi(os.Getenv(checkpointEnv))
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		auth.now = func() time.Time {
			calls++
			if calls == checkpoint {
				// Abrupt exit: do not close stages, storage, SQLite or the transaction.
				os.Exit(0)
			}
			return now
		}
		library, err := NewMediaLibrary(auth)
		if err != nil {
			t.Fatal(err)
		}
		storage, err := s.openMediaStorage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = storage.upload(ctx, library, string(token), "anonymous-upload-exit", "image/png", privateUploadPNG(t, "private"))
		if errors.Is(err, errMediaAnonymousUnsupported) {
			os.Exit(77)
		}
		t.Fatal("upload did not reach the requested interruption", checkpoint, calls, err)
	}
	for _, checkpoint := range []struct {
		name              string
		clock             int
		pending, stages   int
		wantObjectPresent bool
	}{
		{"before journal", 2, 0, 0, false},
		{"after journal", 6, 2, 0, false},
		{"after promotion", 7, 2, 2, true},
	} {
		t.Run(checkpoint.name, func(t *testing.T) {
			s, _, now, _, token := completedAuthFixture(t)
			storage := testMediaStorage(t, s)
			path := s.path
			if err := errors.Join(storage.Close(), s.Close()); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStagedMediaUploadRecoversAnonymousProducerExits$")
			cmd.Env = append(os.Environ(), databaseEnv+"="+path, clockEnv+"="+now.Format(time.RFC3339Nano), checkpointEnv+"="+strconv.Itoa(checkpoint.clock))
			cmd.Stdin = strings.NewReader(token)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("anonymous staging or linking is unavailable on this filesystem")
			}
			if err != nil {
				t.Fatalf("upload producer failed: %v %s", err, output)
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
			if err != nil || len(ids) != checkpoint.pending || next != "" {
				t.Fatal("producer exit left unexpected journal rows", ids, next, err)
			}
			stages, err := os.ReadDir(filepath.Join(owner.path, "staging"))
			if err != nil || len(stages) != checkpoint.stages+1 {
				t.Fatal("producer exit left unexpected named stages", stages, err)
			}
			objects, err := os.ReadDir(filepath.Join(owner.path, "objects"))
			if err != nil || (len(objects) > 1) != checkpoint.wantObjectPresent {
				t.Fatal("producer exit left unexpected promoted objects", objects, err)
			}
			if scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media") != 0 ||
				scalar[int](t, reopened.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='post_media'") != 0 {
				t.Fatal("interrupted upload committed partial source data")
			}
			before := domainState(t, reopened)
			completed, more, err := owner.recoverPendingMediaBatch(t.Context(), 10)
			if err != nil || completed != checkpoint.pending || more {
				t.Fatal("interrupted upload could not be recovered", completed, more, err)
			}
			requireDomainState(t, reopened, before)
			for _, dir := range []string{"staging", "objects"} {
				entries, err := os.ReadDir(filepath.Join(owner.path, dir))
				if err != nil || len(entries) != 1 || entries[0].Name() != mediaOwnerName {
					t.Fatal("recovery retained an unexpected stage or object", dir, entries, err)
				}
			}
		})
	}
}
