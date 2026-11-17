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

	"golang.org/x/sys/unix"
)

func TestJournaledAnonymousMediaRecoversAfterLinkAndPromotionExit(t *testing.T) {
	const databaseEnv = "HOMEBOX_ANONYMOUS_LINK_EXIT_DB"
	const clockEnv = "HOMEBOX_ANONYMOUS_LINK_EXIT_CLOCK"
	const modeEnv = "HOMEBOX_ANONYMOUS_LINK_EXIT_MODE"
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
		pending, err := library.journalMediaObjects(ctx, string(token), stages, storage.objects)
		if err != nil || len(pending) != 2 {
			t.Fatal("could not commit journal before linking", pending, err)
		}
		_, err = runDomainMutation(ctx, auth, string(token), "anonymous-link-crash", "test_link_crash", struct{}{}, func(m *mutationTx) (ActionAck, error) {
			if err := m.materializeAnonymousMediaStages(stages, pending, storage.objects); err != nil {
				return ActionAck{}, err
			}
			if os.Getenv(modeEnv) == "promote" {
				if _, err := stages[0].promoteTo(storage.objects); err != nil {
					return ActionAck{}, err
				}
			}
			// Exit before the publication transaction can commit or defer cleanup.
			os.Exit(0)
			return ActionAck{}, nil
		})
		if errors.Is(err, errMediaAnonymousUnsupported) {
			os.Exit(77)
		}
		t.Fatal("producer did not exit at the selected checkpoint", err)
	}
	for _, mode := range []string{"link", "promote"} {
		t.Run(mode, func(t *testing.T) {
			s, _, now, _, token := completedAuthFixture(t)
			storage := testMediaStorage(t, s)
			path := s.path
			if err := errors.Join(storage.Close(), s.Close()); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJournaledAnonymousMediaRecoversAfterLinkAndPromotionExit$")
			cmd.Env = append(os.Environ(), databaseEnv+"="+path, clockEnv+"="+now.Format(time.RFC3339Nano), modeEnv+"="+mode)
			cmd.Stdin = strings.NewReader(token)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 77 {
				t.Skip("anonymous inode linking is unavailable on this filesystem")
			}
			if err != nil {
				t.Fatalf("anonymous link child failed: %v %s", err, output)
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
			if scalar[int](t, reopened.db, "SELECT COUNT(*) FROM media") != 0 ||
				scalar[int](t, reopened.db, "SELECT COUNT(*) FROM mutation_events WHERE operation='test_link_crash'") != 0 {
				t.Fatal("producer exit committed a partial publication")
			}
			objectCount := 0
			for _, id := range ids {
				row, err := loadMediaPendingRow(t.Context(), reopened.db, id)
				if err != nil {
					t.Fatal(err)
				}
				var stage unix.Stat_t
				if err = unix.Lstat(filepath.Join(owner.path, "staging", row.StageName), &stage); err != nil || int64(stage.Ino) != row.StageInode {
					t.Fatal("named stage lost its committed inode proof", row, stage, err)
				}
				var object unix.Stat_t
				err = unix.Lstat(filepath.Join(owner.path, "objects", "sha256-"+row.SHA256), &object)
				if err == nil {
					if !sameMediaFile(stage, object) || stage.Nlink != 2 {
						t.Fatal("promoted object is not linked to its proven stage")
					}
					objectCount++
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			if mode == "link" && objectCount != 0 || mode == "promote" && objectCount != 1 {
				t.Fatal("producer did not stop at the expected checkpoint", mode, objectCount)
			}
			before := domainState(t, reopened)
			completed, more, err := owner.recoverPendingMediaBatch(t.Context(), 10)
			if err != nil || completed != 2 || more {
				t.Fatal("producer exit was not recoverable", completed, more, err)
			}
			requireDomainState(t, reopened, before)
			for _, dir := range []string{"staging", "objects"} {
				entries, err := os.ReadDir(filepath.Join(owner.path, dir))
				if err != nil || len(entries) != 1 || entries[0].Name() != mediaOwnerName {
					t.Fatal("recovery retained a proven unreferenced stage or object", dir, entries, err)
				}
			}
		})
	}
}
