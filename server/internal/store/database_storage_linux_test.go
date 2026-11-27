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

func TestDatabaseStorageCheckedSizes(t *testing.T) {
	for _, test := range []struct {
		name                    string
		blockSize, fragmentSize int64
		blocks, available       uint64
		total, free             int64
		valid                   bool
	}{
		{"ordinary", 4096, 4096, 100, 40, 409600, 163840, true},
		{"distinct fragment size", 4096, 1024, 100, 40, 102400, 40960, true},
		{"missing fragment size", 4096, 0, 100, 40, 409600, 163840, true},
		{"empty", 4096, 4096, 0, 0, 0, 0, true},
		{"safe maximum", 1, 1, uint64(maxSafeInteger), uint64(maxSafeInteger), maxSafeInteger, maxSafeInteger, true},
		{"zero block size", 0, 1, 1, 0, 0, 0, false},
		{"negative block size", -1, 1, 1, 0, 0, 0, false},
		{"negative fragment size", 4096, -1, 1, 0, 0, 0, false},
		{"free exceeds total", 4096, 4096, 1, 2, 0, 0, false},
		{"unsafe product", 2, 2, uint64(maxSafeInteger), 1, 0, 0, false},
		{"unsafe block size", maxSafeInteger + 1, 1, 1, 0, 0, 0, false},
		{"unsafe fragment size", 1, maxSafeInteger + 1, 1, 0, 0, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			total, free, err := databaseFilesystemBytes(test.blockSize, test.fragmentSize, test.blocks, test.available)
			if test.valid && (err != nil || total != test.total || free != test.free) || !test.valid && !errors.Is(err, ErrDomainState) {
				t.Fatal("invalid filesystem byte calculation", total, free, err)
			}
		})
	}
	for _, test := range []struct {
		total, size int64
		want        int64
		valid       bool
	}{
		{0, 0, 0, true}, {1, maxSafeInteger - 1, maxSafeInteger, true},
		{maxSafeInteger, 1, 0, false}, {-1, 1, 0, false}, {1, -1, 0, false},
	} {
		value, err := addDatabaseFileBytes(test.total, test.size)
		if test.valid && (err != nil || value != test.want) || !test.valid && !errors.Is(err, ErrDomainState) {
			t.Fatal("invalid file byte calculation", test, value, err)
		}
	}
}

func TestDatabaseStorageMeasuresOnlyPrivateRegularFiles(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*testing.T, string)
		valid bool
	}{
		{"regular files", func(*testing.T, string) {}, true},
		{"missing database", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"permissive sidecar", func(t *testing.T, path string) {
			if err := os.Chmod(path+"-wal", 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"linked sidecar", func(t *testing.T, path string) {
			if err := os.Link(path+"-wal", path+"-extra"); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"symlink sidecar", func(t *testing.T, path string) {
			if err := os.Remove(path + "-wal"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path, path+"-wal"); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"directory sidecar", func(t *testing.T, path string) {
			if err := os.Remove(path + "-wal"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path+"-wal", 0o700); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-test-database.sqlite")
			for suffix, data := range map[string]string{"": "abc", "-wal": "1234", "-shm": "xy"} {
				if err := os.WriteFile(path+suffix, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			media := path + ".media"
			if err := os.Mkdir(media, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(media, "synthetic-sentinel"), []byte("not a database sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
			test.alter(t, path)
			value, err := databaseFileBytes(t.Context(), path)
			if test.valid && (err != nil || value != 9) || !test.valid && (!errors.Is(err, ErrUnsafePath) || value != 0) {
				t.Fatal("unsafe or unexpected file measurement", value, err)
			}
			if err != nil && strings.Contains(err.Error(), path) {
				t.Fatal("private path exposed by measurement", err)
			}
			if content, readErr := os.ReadFile(filepath.Join(media, "synthetic-sentinel")); readErr != nil || string(content) != "not a database sidecar" {
				t.Fatal("measurement changed unrelated synthetic Media", readErr)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := databaseFileBytes(ctx, "unused"); !errors.Is(err, context.Canceled) || value != 0 {
		t.Fatal("canceled file measurement returned data", value, err)
	}
}

// This subprocess uses a disposable non-SQLite inode; it never opens another
// descriptor on the active database and tests real process-scoped POSIX locks.
func TestDatabaseStorageLockProbeChild(t *testing.T) {
	if os.Getenv("HOMEBOX_TEST_STORAGE_LOCK_CHILD") != "1" {
		t.Skip("subprocess probe only")
	}
	var info unix.Stat_t
	if err := unix.Fstat(3, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatal("missing disposable lock descriptor", err)
	}
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 1}
	err := unix.FcntlFlock(3, unix.F_SETLK, &lock)
	if os.Getenv("HOMEBOX_TEST_STORAGE_EXPECT_FREE") == "1" {
		if err != nil {
			t.Fatal("expected the disposable lock to be released", err)
		}
		return
	}
	if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
		t.Fatal("the other process could acquire a held POSIX lock", err)
	}
}

func TestDatabaseStorageMetadataDoesNotReleasePOSIXLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-locked-file")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 1}
	if err = unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock); err != nil {
		t.Fatal(err)
	}
	probe := func(expectFree bool) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestDatabaseStorageLockProbeChild$")
		cmd.Env = []string{"HOMEBOX_TEST_STORAGE_LOCK_CHILD=1"}
		if expectFree {
			cmd.Env = append(cmd.Env, "HOMEBOX_TEST_STORAGE_EXPECT_FREE=1")
		}
		cmd.ExtraFiles = []*os.File{file}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("lock probe failed: %v: %s", err, output)
		}
	}
	probe(false)
	if value, err := databaseFileBytes(t.Context(), path); err != nil || value != 0 {
		t.Fatal("metadata-only measurement failed", value, err)
	}
	probe(false)
	// Control experiment: closing a second descriptor on this test inode really
	// does release process-scoped locks, even while the original is still open.
	second, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	probe(true)
}

func TestDatabaseStorageReadsLocalFilesystemWithCompletedSession(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	before := auth.now().UTC()
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if err != nil || value.DatabaseBytes <= 0 || value.AvailableBytes < 0 || value.TotalBytes < value.AvailableBytes {
		t.Fatal("database storage observation was unavailable or inconsistent", value, err)
	}
	measured, err := time.Parse(time.RFC3339Nano, value.MeasuredAt)
	if err != nil || measured.Before(before) || measured.After(auth.now().UTC()) {
		t.Fatal("invalid observation time", value.MeasuredAt, err)
	}
	var file unix.Stat_t
	if err = unix.Lstat(s.path, &file); err != nil || value.DatabaseBytes < file.Size {
		t.Fatal("database observation omitted its main file", value.DatabaseBytes, file.Size, err)
	}
	var filesystem unix.Statfs_t
	if err = unix.Statfs(s.path, &filesystem); err != nil {
		t.Fatal(err)
	}
	total, _, err := databaseFilesystemBytes(filesystem.Bsize, filesystem.Frsize, filesystem.Blocks, filesystem.Bavail)
	if err != nil || value.TotalBytes != total {
		t.Fatal("database filesystem capacity changed unexpectedly", value, total, err)
	}
}

func TestDatabaseStorageRequiresAndRevalidatesSession(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	zero := DatabaseStorage{}
	if value, err := auth.ReadDatabaseStorage(t.Context(), "invalid-token"); !errors.Is(err, ErrAuthentication) || value != zero {
		t.Fatal("unauthenticated storage read returned data", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
	if value, err := auth.ReadDatabaseStorage(t.Context(), token); !errors.Is(err, ErrPasswordChangeRequired) || value != zero {
		t.Fatal("incomplete session returned data", value, err)
	}
	execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=0")
	for _, test := range []struct {
		name, timing, statement string
		want                    error
	}{
		{"ignored activity", "BEFORE", "SELECT RAISE(IGNORE)", ErrDomainState},
		{"revoked", "AFTER", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch changed", "AFTER", "UPDATE sync_state SET dataset_epoch='changed-during-storage-read' WHERE singleton=1", ErrAuthentication},
		{"password changed", "AFTER", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			execSQL(t, s.db, "CREATE TRIGGER database_storage_late "+test.timing+" UPDATE OF last_seen_at ON sessions BEGIN "+test.statement+"; END")
			defer execSQL(t, s.db, "DROP TRIGGER database_storage_late")
			before := domainState(t, s)
			value, err := auth.ReadDatabaseStorage(t.Context(), token)
			if !errors.Is(err, test.want) || value != zero {
				t.Fatal("storage read survived failed session revalidation", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if value, err := auth.ReadDatabaseStorage(ctx, token); !errors.Is(err, context.Canceled) || value != zero {
		t.Fatal("canceled storage read returned data", value, err)
	}
	if value, err := auth.ReadDatabaseStorage(nil, token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("nil context was accepted", value, err)
	}
	var absent *Auth
	if value, err := absent.ReadDatabaseStorage(t.Context(), token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("nil authentication was accepted", value, err)
	}
	if value, err := (&Auth{}).ReadDatabaseStorage(t.Context(), token); !errors.Is(err, ErrDomainInput) || value != zero {
		t.Fatal("authentication without a store was accepted", value, err)
	}
}

func TestDatabaseStorageCancellationWhileWaitingForSQLite(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	held, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	value, err := auth.ReadDatabaseStorage(ctx, token)
	if !errors.Is(err, context.DeadlineExceeded) || value != (DatabaseStorage{}) {
		t.Fatal("a canceled waiting read returned storage data", value, err)
	}
}

func TestDatabaseStorageWithholdsResultsWhenCommitFails(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	execSQL(t, s.db, "CREATE TABLE database_storage_commit_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
	execSQL(t, s.db, "CREATE TRIGGER database_storage_commit_failure AFTER UPDATE OF last_seen_at ON sessions BEGIN INSERT INTO database_storage_commit_guard VALUES('missing'); END")
	before := domainState(t, s)
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if err == nil || value != (DatabaseStorage{}) {
		t.Fatal("failed commit returned storage data", value, err)
	}
	requireDomainState(t, s, before)
	if count := scalar[int](t, s.db, "SELECT COUNT(*) FROM database_storage_commit_guard"); count != 0 {
		t.Fatal("failed commit retained a late write", count)
	}
}

func TestDatabaseStorageRejectsExpiryBeforeReturning(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	before := domainState(t, s)
	base, calls := auth.now().UTC(), 0
	auth.now = func() time.Time {
		calls++
		if calls >= 5 {
			return base.Add(auth.lifetime + time.Minute)
		}
		return base
	}
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if !errors.Is(err, ErrAuthentication) || value != (DatabaseStorage{}) || calls < 5 {
		t.Fatal("expired session received storage data", value, err, calls)
	}
	requireDomainState(t, s, before)
}

func TestDatabaseStorageRejectsUnsafeSidecar(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	journal := s.path + "-journal"
	if _, err := os.Lstat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected no journal in disposable fixture", err)
	}
	target := filepath.Join(filepath.Dir(s.path), "synthetic-sidecar-target")
	if err := os.WriteFile(target, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, journal); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(journal); err != nil {
			t.Error(err)
		}
	}()
	before := domainState(t, s)
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if !errors.Is(err, ErrUnsafePath) || value != (DatabaseStorage{}) || strings.Contains(err.Error(), s.path) {
		t.Fatal("unsafe sidecar returned data or its private path", value, err)
	}
	requireDomainState(t, s, before)
}

func TestDatabaseStorageRejectsInvalidMeasurementTime(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	before := domainState(t, s)
	base, calls := auth.now().UTC(), 0
	auth.now = func() time.Time {
		calls++
		if calls == 4 {
			return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		return base
	}
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if !errors.Is(err, ErrDomainState) || value != (DatabaseStorage{}) || calls != 4 {
		t.Fatal("invalid measurement time returned storage data", value, err, calls)
	}
	requireDomainState(t, s, before)
}

func TestDatabaseStorageRejectsLateDatabasePathReplacement(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	before := domainState(t, s)
	base, calls := auth.now().UTC(), 0
	moved := filepath.Join(filepath.Dir(s.path), "late-original-test-database")
	var moveErr error
	movedOK := false
	defer func() {
		if movedOK {
			if restoreErr := os.Rename(moved, s.path); restoreErr != nil {
				t.Error("restore disposable database", restoreErr)
			}
		}
	}()
	auth.now = func() time.Time {
		calls++
		if calls == 5 {
			moveErr = os.Rename(s.path, moved)
			movedOK = moveErr == nil
		}
		return base
	}
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if moveErr != nil {
		t.Fatal("move disposable database", moveErr)
	}
	if calls < 5 {
		t.Fatal("the late identity check was not reached", calls, value, err)
	}
	if restoreErr := os.Rename(moved, s.path); restoreErr != nil {
		t.Fatal("restore disposable database", restoreErr)
	}
	movedOK = false
	if !errors.Is(err, ErrUnsafePath) || value != (DatabaseStorage{}) {
		t.Fatal("late database path replacement returned data", value, err)
	}
	requireDomainState(t, s, before)
}

func TestDatabaseStorageRejectsChangedDatabaseIdentity(t *testing.T) {
	s, _, auth, token := locationFixture(t)
	moved := filepath.Join(filepath.Dir(s.path), "original-test-database")
	if err := os.Rename(s.path, moved); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(moved, s.path); err != nil {
			t.Error("restore test database", err)
		}
	}()
	value, err := auth.ReadDatabaseStorage(t.Context(), token)
	if !errors.Is(err, ErrUnsafePath) || value != (DatabaseStorage{}) {
		t.Fatal("missing original database identity returned data", value, err)
	}
}
