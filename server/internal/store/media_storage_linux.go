//go:build linux

package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	mediaOwnerName = ".homebox-owner"
	mediaLockName  = ".homebox.lock"
)

var (
	errMediaStorageLocked = errors.New("media storage is already owned by another process")
	errMediaStorageClosed = errors.New("media storage is closed")
)

// mediaStorage is an exclusive private filesystem scope for one database.
// It does not itself recover pending files or authorize any upload.
type mediaStorage struct {
	mu       sync.Mutex
	store    *Store
	path     string
	instance string
	root     *os.File
	staging  *os.File
	objects  *os.File
	lock     *os.File
	closed   bool
	closeErr error
}

func validMediaInstance(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func mediaInstance(ctx context.Context, query queryer) (string, error) {
	var instance string
	err := query.QueryRowContext(ctx, "SELECT server_instance_id FROM sync_state WHERE singleton=1").Scan(&instance)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !validMediaInstance(instance) {
		return "", ErrDomainState
	}
	return instance, err
}

func (s *Store) openMediaStorage(ctx context.Context) (_ *mediaStorage, finalErr error) {
	if s == nil || s.db == nil || s.lock == nil || s.path == "" || ctx == nil {
		return nil, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	instance, err := mediaInstance(ctx, s.db)
	if err != nil {
		return nil, err
	}
	path := s.path + ".media"
	if err = os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	root, err := openPrivateMediaDirectory(path)
	if err != nil {
		return nil, err
	}
	storage := &mediaStorage{store: s, path: path, instance: instance, root: root}
	defer func() {
		if finalErr != nil {
			finalErr = errors.Join(finalErr, storage.Close())
		}
	}()
	storage.lock, err = lockMediaStorage(root)
	if err != nil {
		return nil, err
	}
	if err = ensureMediaOwner(ctx, root, instance); err != nil {
		return nil, err
	}
	storage.staging, err = openMediaSubdirectory(root, "staging")
	if err != nil {
		return nil, err
	}
	storage.objects, err = openMediaSubdirectory(root, "objects")
	if err != nil {
		return nil, err
	}
	stagingID, err := mediaDirectoryID(storage.staging)
	if err != nil {
		return nil, err
	}
	objectID, err := mediaDirectoryID(storage.objects)
	if err != nil {
		return nil, err
	}
	if stagingID == objectID || stagingID.device != objectID.device {
		return nil, ErrUnsafePath
	}
	if err = storage.validate(ctx); err != nil {
		return nil, err
	}
	return storage, nil
}

func lockMediaStorage(root *os.File) (_ *os.File, finalErr error) {
	fd, err := unix.Openat(int(root.Fd()), mediaLockName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(root.Fd()), mediaLockName, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, errors.Join(ErrUnsafePath, err)
	}
	file := os.NewFile(uintptr(fd), "media-storage-lock")
	defer func() {
		if finalErr != nil {
			finalErr = errors.Join(finalErr, file.Close())
		}
	}()
	if created {
		if err = file.Chmod(0o600); err != nil {
			return nil, err
		}
		if err = errors.Join(file.Sync(), root.Sync()); err != nil {
			return nil, err
		}
	}
	if err = verifyMediaLock(root, file); err != nil {
		return nil, err
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.Join(errMediaStorageLocked, err)
		}
		return nil, err
	}
	return file, nil
}

func verifyMediaLock(root, file *os.File) error {
	if root == nil || file == nil {
		return ErrUnsafePath
	}
	var open, named unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &open); err != nil {
		return err
	}
	if err := unix.Fstatat(int(root.Fd()), mediaLockName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(ErrUnsafePath, err)
	}
	if !sameMediaFile(open, named) || open.Mode&0o7777 != 0o600 || open.Nlink != 1 || open.Size != 0 {
		return ErrUnsafePath
	}
	return checkPrivateMediaDirectory(root)
}

func mediaOwnerBytes(instance string) []byte { return []byte("homebox-media-v1\n" + instance + "\n") }

func validMediaOwnerPartial(name string) bool {
	const prefix, suffix = ".owner-", ".partial"
	return len(name) == len(prefix)+32+len(suffix) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) &&
		validMediaStageName("media-"+name[len(prefix):len(prefix)+32]+".partial")
}

// readMediaOwnerFile never follows a link and confirms both the bytes and named inode.
func readMediaOwnerFile(root *os.File, name string, expected []byte, prefix bool) (finalErr error) {
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "media-storage-owner")
	defer func() { finalErr = errors.Join(finalErr, file.Close()) }()
	var open, named unix.Stat_t
	if err = unix.Fstat(fd, &open); err != nil {
		return err
	}
	if err = unix.Fstatat(int(root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !sameMediaFile(open, named) || open.Mode&0o7777 != 0o600 && open.Mode&0o7777 != 0o400 || open.Size < 0 || open.Size > int64(len(expected)) {
		return ErrUnsafePath
	}
	value, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil {
		return err
	}
	if prefix && !bytes.HasPrefix(expected, value) || !prefix && !bytes.Equal(expected, value) {
		return ErrUnsafePath
	}
	if err = unix.Fstatat(int(root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	var after unix.Stat_t
	if err = unix.Fstat(fd, &after); err != nil {
		return err
	}
	if !sameMediaFile(open, named) || !sameMediaFile(open, after) || after.Mode&0o7777 != open.Mode&0o7777 || after.Size != open.Size {
		return ErrUnsafePath
	}
	return checkPrivateMediaDirectory(root)
}

func ensureMediaOwner(ctx context.Context, root *os.File, instance string) error {
	expected := mediaOwnerBytes(instance)
	err := readMediaOwnerFile(root, mediaOwnerName, expected, false)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return errors.Join(ErrUnsafePath, err)
	}
	if _, err = root.Seek(0, io.SeekStart); err != nil {
		return err
	}
	count := 0
	for {
		entries, readErr := root.ReadDir(64)
		count += len(entries)
		if count > 128 {
			return ErrUnsafePath
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			if entry.Name() == mediaLockName {
				continue
			}
			if !validMediaOwnerPartial(entry.Name()) {
				return ErrUnsafePath
			}
			if err = readMediaOwnerFile(root, entry.Name(), expected, true); err != nil {
				return errors.Join(ErrUnsafePath, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return createMediaOwner(ctx, root, expected)
}

func createMediaOwner(ctx context.Context, root *os.File, expected []byte) (finalErr error) {
	for range 8 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		name := ".owner-" + hex.EncodeToString(random[:]) + ".partial"
		fd, err := unix.Openat(int(root.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), "media-owner-stage")
		defer func() { finalErr = errors.Join(finalErr, file.Close()) }()
		if err = file.Chmod(0o600); err != nil {
			return err
		}
		written, err := file.Write(expected)
		if err != nil {
			return err
		}
		if written != len(expected) {
			return io.ErrShortWrite
		}
		if err = file.Sync(); err != nil {
			return err
		}
		var staged, named unix.Stat_t
		if err = unix.Fstat(fd, &staged); err != nil {
			return err
		}
		if err = unix.Fstatat(int(root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if !sameMediaFile(staged, named) || staged.Mode&0o7777 != 0o600 || staged.Nlink != 1 || staged.Size != int64(len(expected)) {
			return ErrUnsafePath
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = unix.Linkat(int(root.Fd()), name, int(root.Fd()), mediaOwnerName, 0); err != nil {
			return err
		}
		// A linked owner marker must be synchronized even after cancellation.
		if err = root.Sync(); err != nil {
			return err
		}
		if err = readMediaOwnerFile(root, mediaOwnerName, expected, false); err != nil {
			return err
		}
		if err = unix.Fstatat(int(root.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameMediaFile(staged, named) {
			return errors.Join(ErrUnsafePath, err)
		}
		if err = unix.Unlinkat(int(root.Fd()), name, 0); err != nil {
			return err
		}
		if err = root.Sync(); err != nil {
			return err
		}
		return ctx.Err()
	}
	return os.ErrExist
}

func openMediaSubdirectory(root *os.File, name string) (_ *os.File, finalErr error) {
	if name != "staging" && name != "objects" {
		return nil, ErrUnsafePath
	}
	if err := unix.Mkdirat(int(root.Fd()), name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrUnsafePath, err)
	}
	dir := os.NewFile(uintptr(fd), "media-private-subdirectory")
	defer func() {
		if finalErr != nil {
			finalErr = errors.Join(finalErr, dir.Close())
		}
	}()
	childID, err := mediaDirectoryID(dir)
	if err != nil {
		return nil, err
	}
	rootID, err := mediaDirectoryID(root)
	if err != nil {
		return nil, err
	}
	if childID.device != rootID.device || childID == rootID {
		return nil, ErrUnsafePath
	}
	var filesystem unix.Statfs_t
	if err = unix.Fstatfs(fd, &filesystem); err != nil {
		return nil, err
	}
	switch uint64(filesystem.Type) {
	case unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC, unix.SMB_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC:
		return nil, ErrUnsafePath
	}
	if err = errors.Join(dir.Sync(), root.Sync()); err != nil {
		return nil, err
	}
	return dir, nil
}

func (storage *mediaStorage) validate(ctx context.Context) error {
	if storage == nil || ctx == nil {
		return ErrDomainInput
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.validateLocked(ctx, storage.store.db)
}

func (storage *mediaStorage) validateLocked(ctx context.Context, query queryer) error {
	if storage.closed {
		return errMediaStorageClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range []struct {
		path string
		dir  *os.File
	}{{storage.path, storage.root}, {filepath.Join(storage.path, "staging"), storage.staging}, {filepath.Join(storage.path, "objects"), storage.objects}} {
		if err := requireMediaDirectoryBinding(entry.path, entry.dir); err != nil {
			return err
		}
	}
	if err := verifyMediaLock(storage.root, storage.lock); err != nil {
		return err
	}
	if err := readMediaOwnerFile(storage.root, mediaOwnerName, mediaOwnerBytes(storage.instance), false); err != nil {
		return errors.Join(ErrUnsafePath, err)
	}
	current, err := mediaInstance(ctx, query)
	if err != nil {
		return err
	}
	if current != storage.instance {
		return ErrDomainState
	}
	return ctx.Err()
}

// upload keeps storage ownership locked throughout preparation and publication.
// HTTP must additionally acquire bounded admission before reading raw bytes.
// ponytail: serialize uploads until recovery can coordinate concurrent producers.
func (storage *mediaStorage) upload(ctx context.Context, library *MediaLibrary, token, key, mime string, raw []byte) (Media, error) {
	if storage == nil || ctx == nil || library == nil || library.auth == nil || library.auth.store != storage.store {
		return Media{}, ErrDomainInput
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.closed {
		return Media{}, errMediaStorageClosed
	}
	guard := func(query queryer) error { return storage.validateLocked(ctx, query) }
	return library.uploadStagedMediaGuarded(ctx, token, key, mime, raw,
		filepath.Join(storage.path, "staging"), filepath.Join(storage.path, "objects"), guard)
}

func (storage *mediaStorage) Close() error {
	if storage == nil {
		return nil
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	if storage.closed {
		return storage.closeErr
	}
	storage.closed = true
	var failures []error
	for _, file := range []*os.File{storage.staging, storage.objects} {
		if file != nil {
			failures = append(failures, file.Close())
		}
	}
	if storage.lock != nil {
		failures = append(failures, unix.Flock(int(storage.lock.Fd()), unix.LOCK_UN), storage.lock.Close())
	}
	if storage.root != nil {
		failures = append(failures, storage.root.Close())
	}
	storage.closeErr = errors.Join(failures...)
	return storage.closeErr
}
