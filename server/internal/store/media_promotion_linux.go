//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var (
	errMediaStageUnsealed   = errors.New("media stage is not sealed")
	errMediaObjectDirectory = errors.New("staging and object directories must differ")
	errMediaObjectInvalid   = errors.New("media object failed integrity verification")
)

type promotedMedia struct {
	key    string
	digest mediaStageDigest
	reused bool
}

func mediaObjectKey(digest [sha256.Size]byte) string {
	return "sha256-" + hex.EncodeToString(digest[:])
}

func sameMediaFile(left, right unix.Stat_t) bool {
	return left.Mode&unix.S_IFMT == unix.S_IFREG && right.Mode&unix.S_IFMT == unix.S_IFREG &&
		left.Dev == right.Dev && left.Ino == right.Ino
}

func validPrivateMediaFile(info os.FileInfo) bool {
	mode := info.Mode()
	return mode.IsRegular() && (mode.Perm() == 0o400 || mode.Perm() == 0o600) && mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

// verifyMediaObject checks actual bytes, size, permissions and the named inode.
// It does not authorize a Media read or establish integrity after it returns.
func verifyMediaObject(ctx context.Context, dir *os.File, key string, expected mediaStageDigest) error {
	return inspectMediaObject(ctx, dir, key, expected, false)
}

func inspectMediaObject(ctx context.Context, dir *os.File, key string, expected mediaStageDigest, syncContent bool) (finalErr error) {
	if ctx == nil {
		return errMediaObjectInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if key != mediaObjectKey(expected.SHA256) || expected.Bytes < 1 || expected.Bytes > maxMediaStageBytes {
		return errMediaObjectInvalid
	}
	if err := checkPrivateMediaDirectory(dir); err != nil {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), key, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(errMediaObjectInvalid, err)
	}
	file := os.NewFile(uintptr(fd), "media-object")
	defer func() { finalErr = errors.Join(finalErr, file.Close()) }()
	var opened, named unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil {
		return err
	}
	if err = unix.Fstatat(int(dir.Fd()), key, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(errMediaObjectInvalid, err)
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !sameMediaFile(opened, named) || !validPrivateMediaFile(info) || info.Size() != expected.Bytes {
		return errMediaObjectInvalid
	}
	if syncContent {
		if err = file.Sync(); err != nil {
			return err
		}
	}
	actual, err := hashMediaFile(ctx, file, expected.Bytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return errors.Join(errMediaObjectInvalid, err)
	}
	if actual != expected.SHA256 {
		return errMediaObjectInvalid
	}
	if err = unix.Fstatat(int(dir.Fd()), key, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(errMediaObjectInvalid, err)
	}
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if !sameMediaFile(opened, named) || !validPrivateMediaFile(info) || info.Size() != expected.Bytes {
		return errMediaObjectInvalid
	}
	if err = checkPrivateMediaDirectory(dir); err != nil {
		return err
	}
	return ctx.Err()
}

// promote links a sealed stage to a content-derived private object without
// replacing an existing name. It does not authorize, publish to SQLite, recover
// abandoned files or delete an object on failure; a caller must discard the
// stage and revalidate file bytes and source authority before publication.
func (stage *mediaStage) promote(directory string) (result promotedMedia, finalErr error) {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.closing || stage.discarded {
		return promotedMedia{}, errMediaStageClosed
	}
	if stage.result == nil {
		return promotedMedia{}, errMediaStageUnsealed
	}
	if err := stage.ctx.Err(); err != nil {
		return promotedMedia{}, err
	}
	if err := stage.checkDirectory(); err != nil {
		return promotedMedia{}, err
	}
	if err := stage.checkName(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return promotedMedia{}, errMediaStageChanged
		}
		return promotedMedia{}, err
	}
	objects, err := openPrivateMediaDirectory(directory)
	if err != nil {
		return promotedMedia{}, err
	}
	defer func() {
		finalErr = errors.Join(finalErr, objects.Close())
		if finalErr != nil {
			result = promotedMedia{}
		}
	}()
	var stagingDir, objectDir unix.Stat_t
	if err = unix.Fstat(int(stage.dir.Fd()), &stagingDir); err != nil {
		return promotedMedia{}, err
	}
	if err = unix.Fstat(int(objects.Fd()), &objectDir); err != nil {
		return promotedMedia{}, err
	}
	if stagingDir.Dev == objectDir.Dev && stagingDir.Ino == objectDir.Ino {
		return promotedMedia{}, errMediaObjectDirectory
	}
	if stagingDir.Dev != objectDir.Dev {
		return promotedMedia{}, unix.EXDEV
	}
	fd, err := unix.Openat(int(stage.dir.Fd()), stage.name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) {
			return promotedMedia{}, errors.Join(errMediaStageChanged, err)
		}
		return promotedMedia{}, err
	}
	file := os.NewFile(uintptr(fd), "media-stage")
	defer func() {
		finalErr = errors.Join(finalErr, file.Close())
		if finalErr != nil {
			result = promotedMedia{}
		}
	}()
	var source unix.Stat_t
	if err = unix.Fstat(fd, &source); err != nil {
		return promotedMedia{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return promotedMedia{}, err
	}
	if source.Mode&unix.S_IFMT != unix.S_IFREG || uint64(source.Dev) != stage.device || source.Ino != stage.inode ||
		!validPrivateMediaFile(info) || info.Size() != stage.result.Bytes || (info.Mode().Perm() == 0o600 && source.Nlink != 1) {
		return promotedMedia{}, errMediaStageChanged
	}
	actual, err := hashMediaFile(stage.ctx, file, stage.result.Bytes)
	if err != nil {
		return promotedMedia{}, err
	}
	if actual != stage.result.SHA256 {
		return promotedMedia{}, errMediaStageChanged
	}
	if err = stage.checkName(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return promotedMedia{}, errMediaStageChanged
		}
		return promotedMedia{}, err
	}
	if info.Mode().Perm() == 0o600 {
		if err = file.Chmod(0o400); err != nil {
			return promotedMedia{}, err
		}
	}
	if err = file.Sync(); err != nil {
		return promotedMedia{}, err
	}
	info, err = file.Stat()
	if err != nil {
		return promotedMedia{}, err
	}
	if !validPrivateMediaFile(info) || info.Mode().Perm() != 0o400 || info.Size() != stage.result.Bytes {
		return promotedMedia{}, errMediaStageChanged
	}
	if err = stage.ctx.Err(); err != nil {
		return promotedMedia{}, err
	}
	if err = stage.checkDirectory(); err != nil {
		return promotedMedia{}, err
	}
	if err = checkPrivateMediaDirectory(objects); err != nil {
		return promotedMedia{}, err
	}
	if err = stage.checkName(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return promotedMedia{}, errMediaStageChanged
		}
		return promotedMedia{}, err
	}
	key := mediaObjectKey(stage.result.SHA256)
	linkErr := unix.Linkat(int(stage.dir.Fd()), stage.name, int(objects.Fd()), key, 0)
	if linkErr != nil && !errors.Is(linkErr, unix.EEXIST) {
		return promotedMedia{}, linkErr
	}
	// Once linked, synchronize the destination even if the caller has canceled;
	// any unreferenced object is retained for a future authoritative recovery.
	if err = errors.Join(objects.Sync(), syncMediaStageParent(objects)); err != nil {
		return promotedMedia{}, err
	}
	if err = stage.ctx.Err(); err != nil {
		return promotedMedia{}, err
	}
	if err = checkPrivateMediaDirectory(objects); err != nil {
		return promotedMedia{}, err
	}
	if err = inspectMediaObject(stage.ctx, objects, key, *stage.result, true); err != nil {
		return promotedMedia{}, err
	}
	if err = checkPrivateMediaDirectory(objects); err != nil {
		return promotedMedia{}, err
	}
	if err = stage.checkDirectory(); err != nil {
		return promotedMedia{}, err
	}
	if err = stage.checkName(); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return promotedMedia{}, errMediaStageChanged
		}
		return promotedMedia{}, err
	}
	if err = stage.ctx.Err(); err != nil {
		return promotedMedia{}, err
	}
	return promotedMedia{key: key, digest: *stage.result, reused: errors.Is(linkErr, unix.EEXIST)}, nil
}
