//go:build linux

package store

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// DatabaseStorage is an observation of the database filesystem, not a Media or
// backup inventory. DatabaseBytes sums logical file lengths, not allocated blocks.
// AvailableBytes does not account for user quotas and can change immediately.
type DatabaseStorage struct {
	DatabaseBytes  int64
	AvailableBytes int64
	TotalBytes     int64
	MeasuredAt     string
}

// ReadDatabaseStorage measures only the owned database path and SQLite sidecars.
// It never opens another descriptor for the active database inode or walks Media.
// The authorization is rechecked after the potentially blocking filesystem reads.
func (a *Auth) ReadDatabaseStorage(ctx context.Context, token string) (DatabaseStorage, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return DatabaseStorage{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return DatabaseStorage{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return DatabaseStorage{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return DatabaseStorage{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return DatabaseStorage{}, err
	}
	// Session activity may trigger SQL changes; observe after it and recheck
	// both session and database-file identity before the transaction commits.
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return DatabaseStorage{}, err
	}
	if err = a.store.requireMediaDatabaseIdentity(); err != nil {
		return DatabaseStorage{}, err
	}
	database, err := databaseFileBytes(ctx, a.store.path)
	if err != nil {
		return DatabaseStorage{}, err
	}
	var filesystem unix.Statfs_t
	if err = unix.Statfs(a.store.path, &filesystem); err != nil {
		return DatabaseStorage{}, err
	}
	if err = ctx.Err(); err != nil {
		return DatabaseStorage{}, err
	}
	total, available, err := databaseFilesystemBytes(filesystem.Bsize, filesystem.Frsize, filesystem.Blocks, filesystem.Bavail)
	if err != nil {
		return DatabaseStorage{}, err
	}
	measured := stamp(a.now().UTC())
	if !validPurchaseInstant(measured) {
		return DatabaseStorage{}, ErrDomainState
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return DatabaseStorage{}, err
	}
	if err = a.store.requireMediaDatabaseIdentity(); err != nil {
		return DatabaseStorage{}, err
	}
	if err = tx.Commit(); err != nil {
		return DatabaseStorage{}, err
	}
	return DatabaseStorage{DatabaseBytes: database, AvailableBytes: available, TotalBytes: total, MeasuredAt: measured}, nil
}

func databaseFilesystemBytes(blockSize, fragmentSize int64, blocks, available uint64) (int64, int64, error) {
	unit, valid := filesystemAllocationUnit(blockSize, fragmentSize)
	if !valid || blockSize > maxSafeInteger || fragmentSize > maxSafeInteger || available > blocks || blocks > uint64(maxSafeInteger)/unit {
		return 0, 0, ErrDomainState
	}
	return int64(blocks * unit), int64(available * unit), nil
}

func addDatabaseFileBytes(total, size int64) (int64, error) {
	if total < 0 || total > maxSafeInteger || size < 0 || size > maxSafeInteger-total {
		return 0, ErrDomainState
	}
	return total + size, nil
}

// Lstat avoids opening/closing a database descriptor, which can release POSIX
// process-scoped SQLite locks. Missing sidecars are not evidence of no WAL data.
func databaseFileBytes(ctx context.Context, path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var info unix.Stat_t
		err := unix.Lstat(path+suffix, &info)
		if suffix != "" && errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return 0, errors.Join(ErrUnsafePath, err)
		}
		if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0o7077 != 0 || info.Nlink != 1 {
			return 0, ErrUnsafePath
		}
		total, err = addDatabaseFileBytes(total, info.Size)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}
