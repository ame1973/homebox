//go:build linux

package store

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func privateDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: data directories require mode 0700", ErrUnsafePath)
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(resolved, &stat); err != nil {
		return "", err
	}
	// SQLite WAL requires local shared-memory semantics, not an SMB/NFS mount.
	switch uint64(stat.Type) {
	case unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC, unix.SMB_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC:
		return "", fmt.Errorf("%w: SQLite data must not use SMB/NFS", ErrUnsafePath)
	}
	return resolved, nil
}

func privateFile(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.Join(ErrUnsafePath, err)
	}
	return file, nil
}

func validateSidecars(path string, snapshot bool) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if snapshot || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("%w: unexpected or permissive SQLite sidecar %s", ErrUnsafePath, suffix)
		}
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	file, err := privateFile(path+".lock", true)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	return file, nil
}

func requireSpace(path string, databaseBytes, reserve uint64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	if stat.Bsize <= 0 || stat.Bavail > math.MaxUint64/uint64(stat.Bsize) || databaseBytes > (math.MaxUint64-reserve)/2 {
		return ErrInsufficientSpace
	}
	available := stat.Bavail * uint64(stat.Bsize)
	if available < databaseBytes*2+reserve {
		return fmt.Errorf("%w: need %d bytes, available %d", ErrInsufficientSpace, databaseBytes*2+reserve, available)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
