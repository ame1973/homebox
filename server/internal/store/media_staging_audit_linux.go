//go:build linux

package store

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const maxMediaStagingAuditEntries = 100_000

var (
	errMediaStagingUnproven = errors.New("media staging contains an unproven entry")
	errMediaStagingLimit    = errors.New("media staging inspection exceeded its entry limit")
)

// auditMediaStaging checks names and inode metadata under the storage owner
// lock. It never removes a file, returns no partial count on failure, and does
// not prove file contents, object references or absence of missing stages.
func (storage *mediaStorage) auditMediaStaging(ctx context.Context, limit int) (count int, finalErr error) {
	if storage == nil || storage.store == nil || storage.store.db == nil || ctx == nil || limit < 1 || limit > maxMediaStagingAuditEntries {
		return 0, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.auditMediaStagingLocked(ctx, limit)
}

func (storage *mediaStorage) auditMediaStagingLocked(ctx context.Context, limit int) (count int, finalErr error) {
	if storage.closed {
		return 0, errMediaStorageClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	tx, err := storage.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = storage.validateLocked(ctx, tx); err != nil {
		return 0, err
	}
	state, err := loadMediaRecoverySyncState(ctx, tx)
	if err != nil {
		return 0, err
	}
	if state.Instance != storage.instance {
		return 0, ErrDomainState
	}
	staging, err := mediaDirectoryID(storage.staging)
	if err != nil {
		return 0, err
	}
	objects, err := mediaDirectoryID(storage.objects)
	if err != nil {
		return 0, err
	}
	statement, err := tx.PrepareContext(ctx, mediaPendingSelect+" WHERE staging_device=? AND staging_inode=? AND stage_name=?")
	if err != nil {
		return 0, err
	}
	defer func() {
		finalErr = errors.Join(finalErr, statement.Close())
		if finalErr != nil {
			count = 0
		}
	}()
	fd, err := unix.Openat(int(storage.staging.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	dir := os.NewFile(uintptr(fd), "media-staging-audit")
	defer func() {
		finalErr = errors.Join(finalErr, dir.Close())
		if finalErr != nil {
			count = 0
		}
	}()
	if actual, err := mediaDirectoryID(dir); err != nil {
		return 0, err
	} else if actual != staging {
		return 0, ErrUnsafePath
	}
	for {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return 0, readErr
		}
		if len(entries) == 0 && readErr == nil {
			return 0, io.ErrNoProgress
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == mediaOwnerName {
				continue
			}
			if count == limit {
				return 0, errMediaStagingLimit
			}
			count++
			if !validMediaStageName(name) {
				return 0, errMediaStagingUnproven
			}
			pending, scanErr := scanMediaPendingRow(statement.QueryRowContext(ctx, staging.device, staging.inode, name))
			if scanErr != nil {
				return 0, errors.Join(errMediaStagingUnproven, scanErr)
			}
			if !validPendingMediaObject(pending) || pending.Epoch != state.Epoch || pending.StageName != name ||
				pending.StagingDevice != staging.device || pending.StagingInode != staging.inode ||
				pending.ObjectDevice != objects.device || pending.ObjectInode != objects.inode {
				return 0, errMediaStagingUnproven
			}
			var actual unix.Stat_t
			if statErr := unix.Fstatat(fd, name, &actual, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
				return 0, errors.Join(errMediaStagingUnproven, statErr)
			}
			if actual.Mode&unix.S_IFMT != unix.S_IFREG || uint64(actual.Dev) != uint64(pending.StageDevice) || actual.Ino != uint64(pending.StageInode) ||
				actual.Size != pending.Bytes || actual.Mode&0o7777 != 0o400 && actual.Mode&0o7777 != 0o600 ||
				actual.Nlink < 1 || actual.Nlink > 2 || actual.Mode&0o7777 == 0o600 && actual.Nlink != 1 {
				return 0, errMediaStagingUnproven
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if err = storage.validateLocked(ctx, tx); err != nil {
		return 0, err
	}
	current, err := loadMediaRecoverySyncState(ctx, tx)
	if err != nil {
		return 0, err
	}
	if current != state {
		return 0, ErrDomainState
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
