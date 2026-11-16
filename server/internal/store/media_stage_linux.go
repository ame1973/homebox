//go:build linux

package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"

	"homebox/internal/mediaimage"

	"golang.org/x/sys/unix"
)

// The input has a separate 32 MiB limit. Re-encoded 16-bit RGBA PNG can be
// larger than its compressed input, so bound the staged output independently.
const maxMediaStageBytes = int64(mediaimage.MaxPixels)*8 + (16 << 20)

var (
	errMediaStageLimit    = errors.New("invalid media staging limit")
	errMediaStageTooLarge = errors.New("staged media exceeds its limit")
	errMediaStageEmpty    = errors.New("staged media is empty")
	errMediaStageChanged  = errors.New("staged media changed while being written")
	errMediaStageClosed   = errors.New("media stage is closed")
)

type mediaStageDigest struct {
	SHA256 [sha256.Size]byte
	Bytes  int64
}

// mediaStage is private groundwork, not publication or authorization. A
// caller must discard it on all paths and recheck its actual bytes before a
// later promotion; a successful seal alone does not prove stored-file safety.
type mediaStage struct {
	mu        sync.Mutex
	ctx       context.Context
	dir       *os.File
	file      *os.File
	name      string
	device    uint64
	inode     uint64
	limit     int64
	written   int64
	hash      hash.Hash
	result    *mediaStageDigest
	failure   error
	closing   bool
	discarded bool
}

func openPrivateMediaDirectory(directory string) (*os.File, error) {
	if directory == "" {
		return nil, ErrUnsafePath
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return nil, errors.Join(ErrUnsafePath, err)
	}
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "media-private-directory")
	info, err := dir.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.Join(ErrUnsafePath, err, dir.Close())
	}
	var filesystem unix.Statfs_t
	if err = unix.Fstatfs(fd, &filesystem); err != nil {
		return nil, errors.Join(err, dir.Close())
	}
	switch uint64(filesystem.Type) {
	case unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC, unix.SMB_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC:
		return nil, errors.Join(ErrUnsafePath, dir.Close())
	}
	if err = syncMediaStageParent(dir); err != nil {
		return nil, errors.Join(err, dir.Close())
	}
	return dir, nil
}

func newMediaStage(ctx context.Context, directory string, limit int64) (*mediaStage, error) {
	if ctx == nil || limit <= 0 || limit > maxMediaStageBytes {
		return nil, errMediaStageLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := openPrivateMediaDirectory(directory)
	if err != nil {
		return nil, err
	}
	fd := int(dir.Fd())
	for range 8 {
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return nil, errors.Join(err, dir.Close())
		}
		name := "media-" + hex.EncodeToString(random[:]) + ".partial"
		fileFD, openErr := unix.Openat(fd, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(openErr, unix.EEXIST) {
			continue
		}
		if openErr != nil {
			return nil, errors.Join(openErr, dir.Close())
		}
		file := os.NewFile(uintptr(fileFD), "media-stage")
		var identity unix.Stat_t
		if err = unix.Fstat(fileFD, &identity); err != nil {
			// Without an inode proof, leave the empty private file for recovery.
			return nil, errors.Join(err, file.Close(), dir.Sync(), dir.Close())
		}
		stage := &mediaStage{ctx: ctx, dir: dir, file: file, name: name,
			device: uint64(identity.Dev), inode: identity.Ino, limit: limit, hash: sha256.New()}
		if err = stage.file.Chmod(0o600); err != nil {
			return nil, errors.Join(err, stage.discard())
		}
		if err = ctx.Err(); err != nil {
			return nil, errors.Join(err, stage.discard())
		}
		return stage, nil
	}
	return nil, errors.Join(os.ErrExist, dir.Close())
}

func syncMediaStageParent(dir *os.File) error {
	fd, err := unix.Openat(int(dir.Fd()), "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent := os.NewFile(uintptr(fd), "media-stage-parent")
	return errors.Join(parent.Sync(), parent.Close())
}

// Write accepts caller-produced output but does not itself validate image
// bytes. Any failed or over-limit write poisons the stage; no partial prefix
// can subsequently be sealed as success.
func (stage *mediaStage) Write(data []byte) (int, error) {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.closing || stage.discarded || stage.result != nil || stage.file == nil {
		return 0, errMediaStageClosed
	}
	if stage.failure != nil {
		return 0, stage.failure
	}
	if err := stage.ctx.Err(); err != nil {
		stage.failure = err
		return 0, err
	}
	if err := stage.checkDirectory(); err != nil {
		stage.failure = err
		return 0, err
	}
	if int64(len(data)) > stage.limit-stage.written {
		stage.failure = errMediaStageTooLarge
		return 0, stage.failure
	}
	n, err := stage.file.Write(data)
	if n > 0 {
		_, _ = stage.hash.Write(data[:n])
		stage.written += int64(n)
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		stage.failure = err
	}
	return n, err
}

func checkPrivateMediaDirectory(dir *os.File) error {
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return ErrUnsafePath
	}
	return nil
}

func (stage *mediaStage) checkDirectory() error {
	return checkPrivateMediaDirectory(stage.dir)
}

// checkName prevents sealing or deleting a different file substituted at the
// generated name. The directory descriptor remains anchored across renames.
func (stage *mediaStage) checkName() error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(stage.dir.Fd()), stage.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFREG || uint64(named.Dev) != stage.device || named.Ino != stage.inode {
		return errMediaStageChanged
	}
	return nil
}

func hashMediaFile(ctx context.Context, file *os.File, expected int64) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if expected < 1 || expected > maxMediaStageBytes {
		return digest, errMediaStageChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return digest, err
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return digest, err
		}
		n, err := file.Read(buffer)
		count += int64(n)
		if count > expected {
			return digest, errMediaStageChanged
		}
		_, _ = hash.Write(buffer[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return digest, err
		}
	}
	if count != expected {
		return digest, errMediaStageChanged
	}
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

// seal synchronizes the file and its directory and checks the actual bytes
// against the bytes accepted by Write. It never promotes or publishes a file.
func (stage *mediaStage) seal() (mediaStageDigest, error) {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.closing || stage.discarded {
		return mediaStageDigest{}, errMediaStageClosed
	}
	if stage.result != nil {
		return *stage.result, nil
	}
	if stage.failure != nil {
		return mediaStageDigest{}, stage.failure
	}
	if err := stage.ctx.Err(); err != nil {
		stage.failure = err
		return mediaStageDigest{}, err
	}
	if stage.written == 0 {
		stage.failure = errMediaStageEmpty
		return mediaStageDigest{}, stage.failure
	}
	fail := func(err error) (mediaStageDigest, error) { stage.failure = err; return mediaStageDigest{}, err }
	if err := stage.checkDirectory(); err != nil {
		return fail(err)
	}
	if err := stage.checkName(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			err = errMediaStageChanged
		}
		return fail(err)
	}
	info, err := stage.file.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != stage.written {
		return fail(errMediaStageChanged)
	}
	if err = stage.file.Sync(); err != nil {
		return fail(err)
	}
	actual, err := hashMediaFile(stage.ctx, stage.file, stage.written)
	if err != nil {
		return fail(err)
	}
	if !bytes.Equal(actual[:], stage.hash.Sum(nil)) {
		return fail(errMediaStageChanged)
	}
	info, err = stage.file.Stat()
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != stage.written {
		return fail(errMediaStageChanged)
	}
	if err = stage.checkName(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			err = errMediaStageChanged
		}
		return fail(err)
	}
	if err = stage.ctx.Err(); err != nil {
		return fail(err)
	}
	err = stage.file.Close()
	stage.file = nil
	if err != nil {
		return fail(err)
	}
	if err = stage.dir.Sync(); err != nil {
		return fail(err)
	}
	if err = syncMediaStageParent(stage.dir); err != nil {
		return fail(err)
	}
	if err = stage.checkDirectory(); err != nil {
		return fail(err)
	}
	if err = stage.ctx.Err(); err != nil {
		return fail(err)
	}
	result := mediaStageDigest{Bytes: stage.written, SHA256: actual}
	stage.result = &result
	return result, nil
}

// discard ignores request cancellation, unlinks only the checked generated name
// and synchronizes the directory. A failed unlink/sync can be retried. Inode
// checking and unlink are not atomic against another process with this UID;
// the private directory is not an authorization boundary for that process.
func (stage *mediaStage) discard() error {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.discarded {
		return nil
	}
	stage.closing = true
	var closeErr error
	if stage.file != nil {
		closeErr = stage.file.Close()
		stage.file = nil
	}
	namedErr := stage.checkName()
	if namedErr != nil && !errors.Is(namedErr, unix.ENOENT) {
		return errors.Join(closeErr, namedErr)
	}
	if namedErr == nil {
		if err := unix.Unlinkat(int(stage.dir.Fd()), stage.name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return errors.Join(closeErr, err)
		}
	}
	if err := stage.dir.Sync(); err != nil {
		return errors.Join(closeErr, err)
	}
	err := stage.dir.Close()
	stage.dir = nil
	stage.discarded = true
	return errors.Join(closeErr, err)
}
