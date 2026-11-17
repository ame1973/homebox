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
	for _, entry := range []struct{ role, directory string }{
		{"root", root}, {"staging", filepath.Join(root, "staging")}, {"objects", filepath.Join(root, "objects")},
	} {
		owner, err := os.ReadFile(filepath.Join(entry.directory, mediaOwnerName))
		expected := mediaOwnerBytes(instance, s.path, s.fileID, entry.role)
		if err != nil || string(owner) != string(expected) {
			t.Fatal("media storage did not retain the actual database and role identity", entry.role, err)
		}
		info, err := os.Lstat(filepath.Join(entry.directory, mediaOwnerName))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatal("storage ownership marker is not private", entry.role, err)
		}
	}
	for _, name := range []string{mediaOwnerName, mediaLockName} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatal("storage ownership metadata is not private", name, err)
		}
	}
	if err := storage.validate(t.Context()); err != nil {
		t.Fatal("new storage did not validate", err)
	}
	if other, err := s.openMediaStorage(t.Context()); !errors.Is(err, errMediaStorageLocked) || other != nil {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("a second owner acquired the same Media storage", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
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

func TestMediaStorageRejectsCopiedInstanceMarkerFromDifferentDatabase(t *testing.T) {
	source := openTestStore(t)
	original := testMediaStorage(t, source)
	target := openTestStore(t)
	instance := scalar[string](t, source.db, "SELECT server_instance_id FROM sync_state")
	execSQL(t, target.db, "UPDATE sync_state SET server_instance_id=? WHERE singleton=1", instance)
	root := target.path + ".media"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(original.path, mediaOwnerName))
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
		t.Fatal("a copied instance ID and marker took ownership of another database's storage", err)
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "preserve" {
		t.Fatal("failed clone validation changed unrelated files", err)
	}
}

func TestMediaStorageRejectsAnotherDatabaseChildAfterReopen(t *testing.T) {
	for _, child := range []string{"staging", "objects"} {
		t.Run(child, func(t *testing.T) {
			source := openTestStore(t)
			original := testMediaStorage(t, source)
			target := openTestStore(t)
			instance := scalar[string](t, source.db, "SELECT server_instance_id FROM sync_state")
			execSQL(t, target.db, "UPDATE sync_state SET server_instance_id=? WHERE singleton=1", instance)
			destination := testMediaStorage(t, target)
			if err := errors.Join(original.Close(), destination.Close()); err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(original.path, child)
			sentinel := filepath.Join(foreign, "keep-me")
			if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			local := filepath.Join(destination.path, child)
			if err := os.Rename(local, local+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(foreign, local); err != nil {
				t.Fatal(err)
			}
			reopened, err := target.openMediaStorage(t.Context())
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrUnsafePath) || reopened != nil {
				t.Fatal("another database's child directory was adopted", err)
			}
			if data, err := os.ReadFile(filepath.Join(local, "keep-me")); err != nil || string(data) != "preserve" {
				t.Fatal("failed child validation changed unrelated files", err)
			}
		})
	}
}

func TestMediaStorageRejectsSwappedChildRolesAfterRestart(t *testing.T) {
	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(storage.path, "staging")
	objects := filepath.Join(storage.path, "objects")
	sentinel := filepath.Join(objects, "keep-me")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, staging+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(objects, staging); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging+"-held", objects); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.openMediaStorage(t.Context())
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrUnsafePath) || reopened != nil {
		t.Fatal("swapped staging and object roles were accepted", err)
	}
	if actual, err := os.ReadFile(filepath.Join(staging, "keep-me")); err != nil || string(actual) != "preserve" {
		t.Fatal("failed role validation modified an unrelated file", err)
	}
}

func TestMediaStorageRejectsReplacedOrHardLinkedDatabase(t *testing.T) {
	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	link := s.path + ".extra-link"
	if err := os.Link(s.path, link); err != nil {
		t.Fatal(err)
	}
	err := storage.validate(t.Context())
	if removeErr := os.Remove(link); removeErr != nil {
		t.Fatal(removeErr)
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatal("a hard-linked database retained storage ownership", err)
	}
	moved := s.path + ".original"
	if err = os.Rename(s.path, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.path, []byte("unrelated replacement"), 0o600); err != nil {
		_ = os.Rename(moved, s.path)
		t.Fatal(err)
	}
	err = storage.validate(t.Context())
	removeErr := os.Remove(s.path)
	restoreErr := os.Rename(moved, s.path)
	if removeErr != nil || restoreErr != nil {
		t.Fatal("failed to restore test-owned database", removeErr, restoreErr)
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatal("a replaced database path retained storage ownership", err)
	}
	if err = storage.validate(t.Context()); err != nil {
		t.Fatal("restored original database did not retain storage ownership", err)
	}
}

func TestMediaStorageDoesNotInitializeAfterDatabasePathReplacement(t *testing.T) {
	s := openTestStore(t)
	moved := s.path + ".original"
	if err := os.Rename(s.path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("unrelated replacement"), 0o600); err != nil {
		_ = os.Rename(moved, s.path)
		t.Fatal(err)
	}
	storage, openErr := s.openMediaStorage(t.Context())
	if storage != nil {
		_ = storage.Close()
	}
	removeErr := os.Remove(s.path)
	restoreErr := os.Rename(moved, s.path)
	if removeErr != nil || restoreErr != nil {
		t.Fatal("failed to restore test-owned database", removeErr, restoreErr)
	}
	if !errors.Is(openErr, ErrUnsafePath) || storage != nil {
		t.Fatal("storage was initialized for a replacement database", openErr)
	}
	if _, err := os.Lstat(s.path + ".media"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected database replacement created a media root", err)
	}
}

func TestMediaStorageReopensWithSameDatabaseFileAfterRestart(t *testing.T) {
	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	path := s.path
	markerPath := filepath.Join(storage.path, mediaOwnerName)
	before, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(storage.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	owner, err := reopened.openMediaStorage(t.Context())
	if err != nil {
		t.Fatal("same database could not reopen its storage", err)
	}
	defer owner.Close()
	if err = owner.validate(t.Context()); err != nil {
		t.Fatal("reopened storage failed identity validation", err)
	}
	if after, err := os.ReadFile(markerPath); err != nil || string(after) != string(before) {
		t.Fatal("ordinary reopen changed ownership metadata", err)
	}
}

func TestMediaStorageRejectsClonedDatabaseAtSamePathAfterRestart(t *testing.T) {
	s := openTestStore(t)
	storage := testMediaStorage(t, s)
	instance := scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state")
	markerPath := filepath.Join(storage.path, mediaOwnerName)
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(storage.path, "objects", "keep-me")
	if err = os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = errors.Join(storage.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	original := s.path + ".original"
	if err = os.Rename(s.path, original); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(clone, source)
	if err = errors.Join(copyErr, clone.Sync(), clone.Close(), source.Close()); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), Config{Path: s.path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if actual := scalar[string](t, reopened.db, "SELECT server_instance_id FROM sync_state"); actual != instance {
		t.Fatal("test copy did not retain the same database instance", actual)
	}
	owner, err := reopened.openMediaStorage(t.Context())
	if owner != nil {
		_ = owner.Close()
	}
	if !errors.Is(err, ErrUnsafePath) || owner != nil {
		t.Fatal("a copied database at the old path inherited private storage", err)
	}
	if actual, err := os.ReadFile(markerPath); err != nil || string(actual) != string(marker) {
		t.Fatal("failed clone adoption changed the original marker", err)
	}
	if actual, err := os.ReadFile(sentinel); err != nil || string(actual) != "preserve" {
		t.Fatal("failed clone adoption changed a retained object", err)
	}
}

func TestMediaStorageRejectsUnmarkedNonemptyChildWithoutDeletingFiles(t *testing.T) {
	for _, child := range []string{"staging", "objects"} {
		t.Run(child, func(t *testing.T) {
			s := openTestStore(t)
			storage := testMediaStorage(t, s)
			if err := storage.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(storage.path, child)
			if err := os.Rename(path, path+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(path, "keep-me")
			if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			reopened, err := s.openMediaStorage(t.Context())
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrUnsafePath) || reopened != nil {
				t.Fatal("an unmarked nonempty child was adopted", err)
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve" {
				t.Fatal("failed adoption modified an unrelated child file", err)
			}
			if _, err := os.Lstat(filepath.Join(path, mediaOwnerName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed adoption created a trusted marker", err)
			}
		})
	}
}

func TestMediaStorageRejectsLegacyOwnerWithoutRebinding(t *testing.T) {
	s := openTestStore(t)
	root := s.path + ".media"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	instance := scalar[string](t, s.db, "SELECT server_instance_id FROM sync_state")
	marker := []byte("homebox-media-v1\n" + instance + "\n")
	path := filepath.Join(root, mediaOwnerName)
	if err := os.WriteFile(path, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(staging, "retained-file")
	if err := os.WriteFile(retained, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := s.openMediaStorage(t.Context())
	if storage != nil {
		_ = storage.Close()
	}
	if !errors.Is(err, ErrUnsafePath) || storage != nil {
		t.Fatal("a legacy instance-only marker was silently rebound", err)
	}
	if actual, err := os.ReadFile(path); err != nil || string(actual) != string(marker) {
		t.Fatal("legacy ownership metadata was modified", err)
	}
	if actual, err := os.ReadFile(retained); err != nil || string(actual) != "preserve" {
		t.Fatal("legacy staged files were modified", err)
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
	if err := os.WriteFile(partial, []byte("homebox-media-v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	if err := storage.validate(t.Context()); err != nil {
		t.Fatal("valid interrupted initialization could not resume", err)
	}
	if data, err := os.ReadFile(partial); err != nil || string(data) != "homebox-media-v2\n" {
		t.Fatal("initialization removed the retained partial marker", err)
	}
}

func TestMediaStorageRecoversInterruptedChildMarkerInitialization(t *testing.T) {
	for _, child := range []string{"staging", "objects"} {
		t.Run(child, func(t *testing.T) {
			s := openTestStore(t)
			storage := testMediaStorage(t, s)
			if err := storage.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(storage.path, child)
			if err := os.Rename(path, path+"-held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			partial := filepath.Join(path, ".owner-"+strings.Repeat("a", 32)+".partial")
			if err := os.WriteFile(partial, []byte("homebox-media-v2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			reopened, err := s.openMediaStorage(t.Context())
			if err != nil {
				t.Fatal("valid interrupted child initialization could not resume", err)
			}
			defer reopened.Close()
			if data, err := os.ReadFile(partial); err != nil || string(data) != "homebox-media-v2\n" {
				t.Fatal("child initialization removed the retained partial marker", err)
			}
		})
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
			owner := mediaOwnerBytes(instance, s.path, s.fileID, "root")
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
				if err := os.Rename(path, path+"-held"); err != nil {
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
	for _, scenario := range []string{"root", "staging", "objects", "owner", "staging owner", "objects owner", "lock", "instance"} {
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
				case "staging owner":
					path = filepath.Join(path, "staging", mediaOwnerName)
				case "objects owner":
					path = filepath.Join(path, "objects", mediaOwnerName)
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

func TestMediaStorageReplayRequiresActualDatabaseIdentity(t *testing.T) {
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	raw := privateUploadPNG(t, "replay database identity")
	first, err := storage.upload(t.Context(), library, token, "replay-file-id", "image/png", raw)
	if err != nil {
		t.Fatal(err)
	}
	link := s.path + ".replay-link"
	if err = os.Link(s.path, link); err != nil {
		t.Fatal(err)
	}
	replay, replayErr := storage.upload(t.Context(), library, token, "replay-file-id", "image/png", raw)
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(replayErr, ErrUnsafePath) || replay != (Media{}) {
		t.Fatal("replay accepted an unbound database path", replay, replayErr)
	}
	execSQL(t, s.db, "CREATE TABLE replay_session_touches(id INTEGER)")
	execSQL(t, s.db, "CREATE TRIGGER replay_session_touch AFTER UPDATE ON sessions BEGIN INSERT INTO replay_session_touches VALUES(1); END")
	clock := auth.now
	for _, changeAt := range []int{3, 4} {
		calls := 0
		var linkErr error
		auth.now = func() time.Time {
			calls++
			if calls == changeAt {
				linkErr = os.Link(s.path, link)
			}
			return clock()
		}
		replay, replayErr = storage.upload(t.Context(), library, token, "replay-file-id", "image/png", raw)
		auth.now = clock
		if calls >= changeAt && linkErr == nil {
			err = os.Remove(link)
		}
		if calls < changeAt || linkErr != nil || err != nil || !errors.Is(replayErr, ErrUnsafePath) || replay != (Media{}) {
			t.Fatal("late database substitution was accepted during replay", changeAt, calls, linkErr, err, replay, replayErr)
		}
		if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM replay_session_touches"); count != 0 {
			t.Fatal("failed replay committed its session activity", count)
		}
	}
	replay, err = storage.upload(t.Context(), library, token, "replay-file-id", "image/png", raw)
	if err != nil || replay != first {
		t.Fatal("restored database could not reconcile the original key", replay, err)
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
	for _, scenario := range []string{"instance", "owner marker", "staging marker", "objects marker", "database hardlink", "owner marker final", "staging marker final", "objects marker final", "database hardlink final"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			base := strings.TrimSuffix(scenario, " final")
			changeAt := 8
			if base != scenario {
				changeAt = 9
			}
			s, auth, _, _, token := completedAuthFixture(t)
			library, err := NewMediaLibrary(auth)
			if err != nil {
				t.Fatal(err)
			}
			storage := testMediaStorage(t, s)
			raw := privateUploadPNG(t, "private owner test")
			before := domainState(t, s)
			var changeErr error
			var markerPath string
			var marker []byte
			switch base {
			case "owner marker":
				markerPath = filepath.Join(storage.path, mediaOwnerName)
			case "staging marker":
				markerPath = filepath.Join(storage.path, "staging", mediaOwnerName)
			case "objects marker":
				markerPath = filepath.Join(storage.path, "objects", mediaOwnerName)
			}
			if markerPath != "" {
				marker, err = os.ReadFile(markerPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			link := s.path + ".late-link"
			calls := 0
			if scenario == "instance" {
				execSQL(t, s.db, `CREATE TRIGGER late_storage_identity AFTER INSERT ON mutation_events WHEN NEW.operation='post_media' BEGIN UPDATE sync_state SET server_instance_id=lower(hex(randomblob(16))) WHERE singleton=1; END`)
			} else {
				clock := auth.now
				auth.now = func() time.Time {
					calls++
					if calls == changeAt {
						if base == "database hardlink" {
							changeErr = os.Link(s.path, link)
						} else {
							changeErr = os.WriteFile(markerPath, []byte("wrong owner"), 0o600)
						}
					}
					return clock()
				}
			}
			result, uploadErr := storage.upload(t.Context(), library, token, "changed-owner-key", "image/png", raw)
			var restoreErr error
			if calls >= changeAt && changeErr == nil {
				if base == "database hardlink" {
					restoreErr = os.Remove(link)
				} else if markerPath != "" {
					restoreErr = os.WriteFile(markerPath, marker, 0o600)
				}
			}
			if uploadErr == nil || result != (Media{}) || changeErr != nil || restoreErr != nil || scenario != "instance" && calls < changeAt {
				t.Fatal("late ownership change was published", scenario, calls, changeErr, restoreErr, result, uploadErr)
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
