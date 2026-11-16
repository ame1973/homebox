package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testMediaStorage(t *testing.T, s *Store) *mediaStorage {
	t.Helper()
	storage, err := s.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal("open owned Media storage", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Error(err)
		}
	})
	return storage
}

func TestMediaStorageCreatesPrivateBoundDirectoriesAndExclusiveLock(t *testing.T) {
	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	instance := scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state")
	root := s.path + ".media"
	if storage.path != root {
		t.Fatal("media storage was not bound to its actual database path")
	}
	for _, path := range []string{root, filepath.Join(root, "staging"), filepath.Join(root, "objects")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			t.Fatal("media directory is not private", path, err)
		}
	}
	owner, err := os.ReadFile(filepath.Join(root, mediaOwnerName))
	if err != nil || string(owner) != "homebox-media-v1\n"+instance+"\n" {
		t.Fatal("media storage did not retain the actual database identity", err)
	}
	for _, name := range []string{mediaOwnerName, mediaLockName} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatal("storage ownership metadata is not private", name, err)
		}
	}
	if err = storage.validate(t.Context()); err != nil {
		t.Fatal("new storage did not validate", err)
	}
	if other, err := s.openMediaStorage(t.Context()); !errors.Is(err, errMediaStorageLocked) || other != nil {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("a second owner acquired the same Media storage", err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal("storage close was not idempotent", err)
	}
	reopened, err := s.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal("closed storage could not be reopened", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaStorageLockExcludesOtherProcessesAndReleasesOnExit(t *testing.T) {
	const directoryEnv = "HOMEBOX_MEDIA_STORAGE_LOCK_TEST_DIR"
	const expectationEnv = "HOMEBOX_MEDIA_STORAGE_LOCK_TEST_EXPECT"
	if directory := os.Getenv(directoryEnv); directory != "" {
		root, err := openPrivateMediaDirectory(directory)
		if err != nil {
			t.Fatal(err)
		}
		file, err := lockMediaStorage(root)
		if os.Getenv(expectationEnv) == "busy" {
			if !errors.Is(err, errMediaStorageLocked) || file != nil {
				t.Fatal("another process acquired the owned storage", err)
			}
		} else if err != nil || file == nil {
			t.Fatal("another process could not acquire released storage", err)
		}
		// Exit without closing the handles to exercise kernel lock release.
		os.Exit(0)
	}

	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	runHelper := func(expectation string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMediaStorageLockExcludesOtherProcessesAndReleasesOnExit$")
		cmd.Env = append(os.Environ(), directoryEnv+"="+storage.path, expectationEnv+"="+expectation)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("storage lock helper failed: %v %s", err, output)
		}
	}
	runHelper("busy")
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	runHelper("free")
	reopened, err := s.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal("terminated process retained the storage lock", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaStorageRefusesAnotherDatabaseOwnerWithoutDeletingFiles(t *testing.T) {
	source := openTestStore(t)
	owner := testMediaStorage(t, source)
	target := openTestStore(t)
	root := target.path + ".media"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(owner.path, mediaOwnerName))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, mediaOwnerName), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "keep-me")
	if err = os.WriteFile(unrelated, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := target.openMediaStorage(t.Context())
	if storage != nil {
		_ = storage.Close()
	}
	if !errors.Is(err, ErrUnsafePath) || storage != nil {
		t.Fatal("storage from another database was accepted", err)
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "preserve" {
		t.Fatal("failed ownership validation changed unrelated files", err)
	}
}

func TestMediaStorageRefusesUnknownInitialContentsWithoutDeletingThem(t *testing.T) {
	s := openTestStore(t)
	root := s.path + ".media"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(root, "important-user-file")
	if err := os.WriteFile(unrelated, []byte("leave untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if storage, err := s.openMediaStorage(t.Context()); !errors.Is(err, ErrUnsafePath) || storage != nil {
		if storage != nil {
			_ = storage.Close()
		}
		t.Fatal("an unowned nonempty directory was adopted", err)
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "leave untouched" {
		t.Fatal("failed initialization changed an unrelated file", err)
	}
	if _, err := os.Lstat(filepath.Join(root, mediaOwnerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed ownership check created a trusted marker", err)
	}
}

func TestMediaStorageRecoversInterruptedOwnerInitializationWithoutDeletingFiles(t *testing.T) {
	s := openTestStore(t)
	root := s.path + ".media"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(root, ".owner-"+strings.Repeat("a", 32)+".partial")
	if err := os.WriteFile(partial, []byte("homebox-media-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	if err := storage.validate(t.Context()); err != nil {
		t.Fatal("valid interrupted initialization could not resume", err)
	}
	if data, err := os.ReadFile(partial); err != nil || string(data) != "homebox-media-v1\n" {
		t.Fatal("initialization removed the retained partial marker", err)
	}
}

func TestMediaStorageBoundsUnownedDirectoryInspection(t *testing.T) {
	s := openTestStore(t)
	root := s.path + ".media"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := range 128 {
		path := filepath.Join(root, fmt.Sprintf(".owner-%032x.partial", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if storage, err := s.openMediaStorage(t.Context()); !errors.Is(err, ErrUnsafePath) || storage != nil {
		if storage != nil {
			_ = storage.Close()
		}
		t.Fatal("unbounded unowned directory inspection was accepted", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 129 {
		t.Fatal("failed bounded inspection removed user entries", len(entries), err)
	}
}

func TestMediaStorageRejectsUnsafeRootAndOwner(t *testing.T) {
	for _, scenario := range []string{"root symlink", "public root", "owner symlink", "owner mismatch", "owner public", "bad partial", "malformed instance"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s := openTestStore(t)
			root := s.path + ".media"
			instance := scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state")
			owner := []byte("homebox-media-v1\n" + instance + "\n")
			if scenario == "root symlink" {
				target := privateTempDir(t)
				if err := os.Symlink(target, root); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "malformed instance" {
				execSQL(t, s.db, `UPDATE sync_state SET server_instance_id='not-a-canonical-id' WHERE singleton=1`)
			} else {
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "public root":
					if err := os.Chmod(root, 0o755); err != nil {
						t.Fatal(err)
					}
				case "owner symlink":
					target := filepath.Join(privateTempDir(t), "owner")
					if err := os.WriteFile(target, owner, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, filepath.Join(root, mediaOwnerName)); err != nil {
						t.Fatal(err)
					}
				case "owner mismatch", "owner public":
					if scenario == "owner mismatch" {
						owner = []byte("unrelated owner")
					}
					path := filepath.Join(root, mediaOwnerName)
					if err := os.WriteFile(path, owner, 0o600); err != nil {
						t.Fatal(err)
					}
					if scenario == "owner public" {
						if err := os.Chmod(path, 0o644); err != nil {
							t.Fatal(err)
						}
					}
				case "bad partial":
					if err := os.WriteFile(filepath.Join(root, ".owner-"+strings.Repeat("a", 32)+".partial"), []byte("not our marker"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			storage, err := s.openMediaStorage(t.Context())
			if storage != nil {
				_ = storage.Close()
			}
			if err == nil || storage != nil {
				t.Fatal("unsafe ownership state was accepted", scenario, err)
			}
			if scenario == "malformed instance" {
				if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("invalid database identity created a media root", statErr)
				}
			}
		})
	}
}

func TestMediaStorageRejectsUnsafeSubdirectoriesOnReopen(t *testing.T) {
	for _, scenario := range []string{"staging symlink", "objects symlink", "staging public", "objects public", "lock symlink", "lock public", "lock contents"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s := openTestStore(t)
			storage := testMediaStorage(t, s)
			root := storage.path
			if err := storage.Close(); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(scenario, " ")
			name := parts[0]
			if name == "lock" {
				name = mediaLockName
			}
			path := filepath.Join(root, name)
			switch parts[1] {
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(privateTempDir(t), path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "contents":
				if err := os.WriteFile(path, []byte("do not overwrite"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := s.openMediaStorage(t.Context())
			if err == nil || reopened != nil {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatal("unsafe storage component was accepted", scenario, err)
			}
			if scenario == "lock contents" {
				if data, err := os.ReadFile(path); err != nil || string(data) != "do not overwrite" {
					t.Fatal("invalid lock contents were modified", err)
				}
			}
		})
	}
}

func TestMediaStoragePreservesUnknownFilesAfterOwnership(t *testing.T) {
	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	root := storage.path
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "unrelated-user-file")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep me" {
		t.Fatal("owned storage initialization removed unrelated files", err)
	}
}

func TestMediaStorageRevalidatesInstanceAndDirectoryIdentities(t *testing.T) {
	for _, scenario := range []string{"root", "staging", "objects", "owner", "lock", "instance"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s := openTestStore(t)
			storage := testMediaStorage(t, s)
			if scenario == "instance" {
				execSQL(t, s.db, `UPDATE sync_state SET server_instance_id=lower(hex(randomblob(16))) WHERE singleton=1`)
			} else {
				path := storage.path
				switch scenario {
				case "staging", "objects":
					path = filepath.Join(path, scenario)
				case "owner":
					path = filepath.Join(path, mediaOwnerName)
				case "lock":
					path = filepath.Join(path, mediaLockName)
				}
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if scenario == "root" || scenario == "staging" || scenario == "objects" {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("unrelated"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := storage.validate(t.Context()); err == nil {
				t.Fatal("changed storage ownership remained valid", scenario)
			}
		})
	}
}

func TestMediaStorageUploadsWithBoundOwnershipAndRetainsOriginalReplay(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	raw := privateUploadPNG(t, "private owner test")
	first, err := storage.upload(t.Context(), library, token, "owned-upload-key", "image/png", raw)
	if err != nil || first.Revision != 1 || first.Verification != "verified" {
		t.Fatal("owned storage did not publish the private transaction", first, err)
	}
	actual, err := loadMedia(t.Context(), s.db, first.ID)
	if err != nil || actual.thumbnail == nil || scalar[int](t, s.db, "SELECT COUNT(*) FROM media_pending_objects") != 0 {
		t.Fatal("owned upload omitted source or retained completed pending rows", actual, err)
	}
	if _, err = os.Stat(filepath.Join(storage.path, "objects", actual.storageKey)); err != nil {
		t.Fatal("uploaded original is not in the owned object directory", err)
	}
	old := filepath.Join(storage.path, "objects")
	if err = os.Rename(old, old+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	replay, err := storage.upload(t.Context(), library, token, "owned-upload-key", "image/png", raw)
	if err != nil || replay != first {
		t.Fatal("original-key reconciliation incorrectly required current files", replay, err)
	}
	if value, err := storage.upload(t.Context(), library, token, "fresh-owned-key", "image/png", raw); err == nil || value != (Media{}) {
		t.Fatal("fresh upload accepted substituted object storage", value, err)
	}
}

func TestMediaStorageRejectsUploadFromAnotherStore(t *testing.T) {
	_, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	other := openTestStore(t)
	storage := testMediaStorage(t, other)
	raw := privateUploadPNG(t, "another database")
	if result, err := storage.upload(t.Context(), library, token, "foreign-store-key", "image/png", raw); !errors.Is(err, ErrDomainInput) || result != (Media{}) {
		t.Fatal("storage accepted an upload from another database", result, err)
	}
	if count := scalar[int](t, other.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 0 {
		t.Fatal("rejected upload changed the other database", count)
	}
}

func TestMediaStorageUploadRollsBackLateOwnershipChanges(t *testing.T) {
	for _, scenario := range []string{"instance", "owner marker"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, auth, _, _, token := completedAuthFixture(t)
			library, err := NewMediaLibrary(auth)
			if err != nil {
				t.Fatal(err)
			}
			storage := testMediaStorage(t, s)
			raw := privateUploadPNG(t, "private owner test")
			before := domainState(t, s)
			var markerErr error
			calls := 0
			if scenario == "instance" {
				execSQL(t, s.db, `CREATE TRIGGER late_storage_identity AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE sync_state SET server_instance_id=lower(hex(randomblob(16))) WHERE singleton=1; END`)
			} else {
				clock := auth.now
				auth.now = func() time.Time {
					calls++
					if calls == 8 {
						markerErr = os.WriteFile(filepath.Join(storage.path, mediaOwnerName), []byte("wrong owner"), 0o600)
					}
					return clock()
				}
			}
			result, err := storage.upload(t.Context(), library, token, "changed-owner-key", "image/png", raw)
			if err == nil || result != (Media{}) || markerErr != nil || scenario == "owner marker" && calls < 8 {
				t.Fatal("late ownership change was published", scenario, calls, markerErr, result, err)
			}
			requireDomainState(t, s, before)
			requirePendingUploadStages(t, s, filepath.Join(storage.path, "staging"), 2)
		})
	}
}

func TestMediaStorageCloseWaitsForActiveUpload(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	clock := auth.now
	calls := 0
	entered, resume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	defer release()
	auth.now = func() time.Time {
		calls++
		if calls == 6 {
			close(entered)
			<-resume
		}
		return clock()
	}
	type outcome struct {
		value Media
		err   error
	}
	uploaded := make(chan outcome, 1)
	raw := privateUploadPNG(t, "private")
	go func() {
		value, err := storage.upload(t.Context(), library, token, "close-during-upload", "image/png", raw)
		uploaded <- outcome{value, err}
	}()
	select {
	case <-entered:
	case result := <-uploaded:
		t.Fatal("upload ended before reaching the protected transaction", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not reach the protected transaction")
	}
	closing := make(chan error, 1)
	go func() { closing <- storage.Close() }()
	select {
	case err := <-closing:
		t.Fatal("storage closed while upload was active", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case result := <-uploaded:
		if result.err != nil || result.value.ID == "" {
			t.Fatal("protected upload did not finish before close", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not finish")
	}
	select {
	case err := <-closing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("storage close did not finish")
	}
	if err = storage.validate(t.Context()); !errors.Is(err, errMediaStorageClosed) {
		t.Fatal("closed storage accepted further work", err)
	}
}

func TestMediaStorageRejectsNilOrCanceledContext(t *testing.T) {
	s := openTestStore(t)
	if storage, err := s.openMediaStorage(nil); !errors.Is(err, ErrDomainInput) || storage != nil {
		t.Fatal("nil context was accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if storage, err := s.openMediaStorage(ctx); !errors.Is(err, context.Canceled) || storage != nil {
		t.Fatal("canceled initialization continued", err)
	}
	if _, err := os.Lstat(s.path + ".media"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid context created a Media root", err)
	}
}
