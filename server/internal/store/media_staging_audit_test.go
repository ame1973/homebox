//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMediaStagingAuditRecognizesJournaledAndMissingStages(t *testing.T) {
	f := newRecoveryFixture(t)
	f.promote(t, 0)
	before := domainState(t, f.store)
	count, err := f.storage.auditMediaStaging(t.Context(), 2)
	if err != nil || count != 2 {
		t.Fatal("journaled stage inodes were not recognized", count, err)
	}
	if err = os.Remove(f.stagePath(1)); err != nil {
		t.Fatal(err)
	}
	if err = syncRecoveryDirectory(f.storage.staging); err != nil {
		t.Fatal(err)
	}
	count, err = f.storage.auditMediaStaging(t.Context(), 2)
	if err != nil || count != 1 || scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects") != 2 {
		t.Fatal("missing stage was treated as an unknown file or silently recovered", count, err)
	}
	requireDomainState(t, f.store, before)
}

func TestMediaStagingAuditRetainsUnknownEntries(t *testing.T) {
	for _, kind := range []string{"generated name", "arbitrary name", "symlink", "hard link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newRecoveryFixture(t)
			outside := filepath.Join(privateTempDir(t), "outside")
			if err := os.WriteFile(outside, []byte("keep unrelated bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			name := "media-" + strings.Repeat("a", 32) + ".partial"
			if kind == "arbitrary name" {
				name = "unexpected\nprivate-name"
			}
			path := filepath.Join(f.storage.path, "staging", name)
			var createErr error
			switch kind {
			case "generated name", "arbitrary name":
				var file *os.File
				file, createErr = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if createErr == nil {
					_, createErr = file.Write([]byte("unknown stage"))
					createErr = errors.Join(createErr, file.Close())
				}
			case "symlink":
				createErr = os.Symlink(outside, path)
			case "hard link":
				createErr = os.Link(outside, path)
			case "directory":
				createErr = os.Mkdir(path, 0o700)
			}
			if createErr != nil {
				t.Fatal(createErr)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			domain := domainState(t, f.store)
			count, err := f.storage.auditMediaStaging(t.Context(), 100)
			if count != 0 || !errors.Is(err, errMediaStagingUnproven) || strings.Contains(err.Error(), name) || strings.Contains(err.Error(), outside) {
				t.Fatal("unknown entry was accepted or exposed in an error", count, err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("audit changed an unknown entry", err)
			}
			if bytes, err := os.ReadFile(outside); err != nil || string(bytes) != "keep unrelated bytes" {
				t.Fatal("audit followed or changed the unrelated file", string(bytes), err)
			}
			requireDomainState(t, f.store, domain)
		})
	}
}

func TestMediaStagingAuditRejectsSubstitutedJournaledInode(t *testing.T) {
	f := newRecoveryFixture(t)
	path := f.stagePath(0)
	retained := path + ".retained"
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(path)
		_ = os.Rename(retained, path)
	}()
	if err := os.WriteFile(path, []byte("unrelated replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, f.store)
	count, err := f.storage.auditMediaStaging(t.Context(), 100)
	if count != 0 || !errors.Is(err, errMediaStagingUnproven) {
		t.Fatal("substituted inode was accepted", count, err)
	}
	if bytes, err := os.ReadFile(path); err != nil || string(bytes) != "unrelated replacement" {
		t.Fatal("audit changed the substituted file", string(bytes), err)
	}
	if _, err := os.Lstat(retained); err != nil {
		t.Fatal("audit removed the original stage", err)
	}
	requireDomainState(t, f.store, before)
}

func TestMediaStagingAuditRejectsStaleOrUnsafeMetadata(t *testing.T) {
	for _, kind := range []string{"stale epoch", "unsafe mode", "extra hard link", "foreign object directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newRecoveryFixture(t)
			switch kind {
			case "stale epoch":
				execSQL(t, f.store.db, "UPDATE sync_state SET dataset_epoch='different-epoch' WHERE singleton=1")
			case "unsafe mode":
				if err := os.Chmod(f.stagePath(0), 0o644); err != nil {
					t.Fatal(err)
				}
			case "extra hard link":
				if err := os.Link(f.stagePath(0), filepath.Join(privateTempDir(t), "other-link")); err != nil {
					t.Fatal(err)
				}
			case "foreign object directory":
				execSQL(t, f.store.db, "DROP TRIGGER media_pending_objects_no_update")
				execSQL(t, f.store.db, "UPDATE media_pending_objects SET object_inode=object_inode+1 WHERE id=?", f.pending[0].ID)
			}
			before := domainState(t, f.store)
			if count, err := f.storage.auditMediaStaging(t.Context(), 2); count != 0 || !errors.Is(err, errMediaStagingUnproven) {
				t.Fatal("stale or unsafe stage metadata was accepted", count, err)
			}
			requireDomainState(t, f.store, before)
			if _, err := os.Lstat(f.stagePath(0)); err != nil {
				t.Fatal("audit removed an unsafe stage", err)
			}
		})
	}
}

func TestMediaStagingAuditIsNotAContentIntegrityCheck(t *testing.T) {
	f := newRecoveryFixture(t)
	value, err := os.ReadFile(f.stagePath(0))
	if err != nil || len(value) == 0 {
		t.Fatal("could not inspect stage fixture", err)
	}
	value[0] ^= 1
	if err = os.WriteFile(f.stagePath(0), value, 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := f.storage.auditMediaStaging(t.Context(), 2); err != nil || count != 2 {
		t.Fatal("metadata-only audit unexpectedly claimed byte verification", count, err)
	}
	if _, err = f.storage.recoverPendingMedia(t.Context(), f.pending[0].ID); !errors.Is(err, errMediaRecoveryUnproven) {
		t.Fatal("recovery accepted altered actual bytes", err)
	}
	if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
		t.Fatal("failed integrity verification changed journal rows", count)
	}
}

func TestMediaStagingAuditBoundsAndCancellation(t *testing.T) {
	var missing *mediaStorage
	if count, err := missing.auditMediaStaging(t.Context(), 1); count != 0 || !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil storage was accepted", count, err)
	}
	f := newRecoveryFixture(t)
	for _, limit := range []int{-1, 0, maxMediaStagingAuditEntries + 1} {
		if count, err := f.storage.auditMediaStaging(t.Context(), limit); count != 0 || !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid audit bound was accepted", limit, count, err)
		}
	}
	if count, err := f.storage.auditMediaStaging(nil, 2); count != 0 || !errors.Is(err, ErrDomainInput) {
		t.Fatal("nil context was accepted", count, err)
	}
	before := domainState(t, f.store)
	if count, err := f.storage.auditMediaStaging(t.Context(), 1); count != 0 || !errors.Is(err, errMediaStagingLimit) {
		t.Fatal("incomplete bounded scan returned a successful prefix", count, err)
	}
	requireDomainState(t, f.store, before)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := f.storage.auditMediaStaging(ctx, 2); count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled audit began work", count, err)
	}
	f.storage.mu.Lock()
	waiting, stop := context.WithCancel(t.Context())
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); _, err := f.storage.auditMediaStaging(waiting, 2); result <- err }()
	<-started
	select {
	case err := <-result:
		f.storage.mu.Unlock()
		t.Fatal("audit did not wait for the owner lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	stop()
	f.storage.mu.Unlock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("canceled audit resumed work", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled audit did not return")
	}
	if err := f.storage.Close(); err != nil {
		t.Fatal(err)
	}
	if count, err := f.storage.auditMediaStaging(t.Context(), 2); count != 0 || !errors.Is(err, errMediaStorageClosed) {
		t.Fatal("closed storage was audited", count, err)
	}
}

func TestMediaStagingAuditChecksAcrossDirectoryReadPages(t *testing.T) {
	s, _, _, _, _ := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	staging, err := mediaDirectoryID(storage.staging)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := mediaDirectoryID(storage.objects)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := loadMediaRecoverySyncState(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(t.Context(), `INSERT INTO media_pending_objects(id,dataset_epoch,sha256,bytes,created_at,stage_name,stage_device,stage_inode,staging_device,staging_inode,object_device,object_inode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	digest := sha256.Sum256([]byte("x"))
	for index := 0; index < 130; index++ {
		name := fmt.Sprintf("media-%032x.partial", index+1)
		fd, err := unix.Openat(int(storage.staging.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(uintptr(fd), "audit-fixture-stage")
		if err = file.Chmod(0o600); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err = file.Write([]byte("x")); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		var info unix.Stat_t
		if err = unix.Fstat(fd, &info); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		if err = requireDomainRow(statement.ExecContext(t.Context(), fmt.Sprintf("audit-stage-%d", index), state.Epoch, hex.EncodeToString(digest[:]), 1, "2026-11-17T00:00:00Z", name,
			int64(info.Dev), int64(info.Ino), staging.device, staging.inode, objects.device, objects.inode)); err != nil {
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if count, err := storage.auditMediaStaging(t.Context(), 129); count != 0 || !errors.Is(err, errMediaStagingLimit) {
		t.Fatal("cross-page limit returned a successful prefix", count, err)
	}
	if count, err := storage.auditMediaStaging(t.Context(), 130); err != nil || count != 130 {
		t.Fatal("audit did not finish every directory page", count, err)
	}
}

func TestMediaStagingAuditRequiresTheActualJournalTable(t *testing.T) {
	s, _, _, _, _ := completedAuthFixture(t)
	storage := testMediaStorage(t, s)
	execSQL(t, s.db, "DROP TABLE media_pending_objects")
	if count, err := storage.auditMediaStaging(t.Context(), 1); err == nil || count != 0 {
		t.Fatal("an empty directory hid a missing journal table", count, err)
	}
}
