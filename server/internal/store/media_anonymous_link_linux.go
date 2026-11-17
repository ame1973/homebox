//go:build linux

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// linkAnonymousMediaFD never replaces a name. Some Linux environments do not
// grant AT_EMPTY_PATH; procfs provides the documented unprivileged alternative.
func linkAnonymousMediaFD(file, directory *os.File, name string) (finalErr error) {
	err := unix.Linkat(int(file.Fd()), "", int(directory.Fd()), name, unix.AT_EMPTY_PATH)
	if err == nil || errors.Is(err, unix.EEXIST) {
		return err
	}
	if !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES) &&
		!errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	fd, openErr := unix.Open("/proc/self/fd", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if openErr != nil {
		return errors.Join(errMediaAnonymousUnsupported, err, openErr)
	}
	proc := os.NewFile(uintptr(fd), "media-proc-fd")
	defer func() { finalErr = errors.Join(finalErr, proc.Close()) }()
	var filesystem unix.Statfs_t
	if statErr := unix.Fstatfs(fd, &filesystem); statErr != nil {
		return errors.Join(errMediaAnonymousUnsupported, statErr)
	}
	if uint64(filesystem.Type) != unix.PROC_SUPER_MAGIC {
		return errors.Join(errMediaAnonymousUnsupported, ErrUnsafePath)
	}
	linkErr := unix.Linkat(fd, strconv.Itoa(int(file.Fd())), int(directory.Fd()), name, unix.AT_SYMLINK_FOLLOW)
	if errors.Is(linkErr, unix.ENOENT) || errors.Is(linkErr, unix.EPERM) || errors.Is(linkErr, unix.EACCES) ||
		errors.Is(linkErr, unix.EINVAL) || errors.Is(linkErr, unix.ENOSYS) || errors.Is(linkErr, unix.EOPNOTSUPP) {
		return errors.Join(errMediaAnonymousUnsupported, linkErr)
	}
	return linkErr
}

// linkJournaledAnonymousStage requires the caller's active SQLite write
// transaction to retain the previously committed journal through publication.
// A linked name is preserved on every later error; recovery owns its cleanup.
func (stage *mediaStage) linkJournaledAnonymousStage(ctx context.Context, tx *sql.Tx, pending pendingMediaObject, objects *os.File) error {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if !stage.anonymous || stage.file == nil {
		return ErrDomainState
	}
	objectID, err := mediaDirectoryID(objects)
	if err != nil {
		return err
	}
	actual, err := journalStageLocked(stage, objectID)
	if err != nil {
		return err
	}
	actual.ID, actual.Epoch, actual.CreatedAt = pending.ID, pending.Epoch, pending.CreatedAt
	if actual != pending {
		return ErrDomainState
	}
	if err = requireMediaPendingEpoch(ctx, tx, pending.Epoch); err != nil {
		return err
	}
	if err = requireMediaPendingRow(ctx, tx, pending); err != nil {
		return err
	}
	expected, err := recoveryDigest(pending)
	if err != nil {
		return err
	}
	digest, err := hashMediaFile(ctx, stage.file, expected.Bytes)
	if err != nil || digest != expected.SHA256 {
		return errors.Join(errMediaStageChanged, err)
	}
	if err = stage.checkName(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	linkErr := linkAnonymousMediaFD(stage.file, stage.dir, stage.name)
	var named unix.Stat_t
	nameErr := unix.Fstatat(int(stage.dir.Fd()), stage.name, &named, unix.AT_SYMLINK_NOFOLLOW)
	matched := nameErr == nil && uint64(named.Dev) == stage.device && named.Ino == stage.inode
	var closeErr error
	if matched {
		// Even an ambiguous link error can leave this journaled inode named.
		stage.anonymous = false
		closeErr = stage.file.Close()
		stage.file = nil
	}
	// Synchronize after every link attempt, including errors and cancellation.
	syncErr := errors.Join(stage.dir.Sync(), syncMediaStageParent(stage.dir))
	if linkErr != nil || nameErr != nil || !matched || closeErr != nil || syncErr != nil {
		return errors.Join(errMediaStageChanged, linkErr, nameErr, closeErr, syncErr, ctx.Err())
	}
	if named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o7777 != 0o600 || named.Nlink != 1 || named.Size != expected.Bytes {
		return errMediaStageChanged
	}
	if err = requireMediaPendingEpoch(ctx, tx, pending.Epoch); err != nil {
		return err
	}
	if err = requireMediaPendingRow(ctx, tx, pending); err != nil {
		return err
	}
	actual, err = journalStageLocked(stage, objectID)
	if err != nil {
		return err
	}
	actual.ID, actual.Epoch, actual.CreatedAt = pending.ID, pending.Epoch, pending.CreatedAt
	if actual != pending {
		return ErrDomainState
	}
	return ctx.Err()
}

// materializeAnonymousMediaStages final-checks the prior committed pair after
// audit and session writes. Call it inside the same mutation that promotes and
// publishes the objects, while the storage owner remains locked.
func (m *mutationTx) materializeAnonymousMediaStages(stages []*mediaStage, pending []pendingMediaObject, objects *os.File) error {
	if m == nil || m.ctx == nil || m.tx == nil || len(stages) != 2 || stages[0] == nil || stages[1] == nil || stages[0] == stages[1] || objects == nil {
		return ErrDomainInput
	}
	pair := append([]pendingMediaObject(nil), pending...)
	if err := m.retainMediaPending(pair); err != nil {
		return err
	}
	for index, stage := range stages {
		if err := stage.linkJournaledAnonymousStage(m.ctx, m.tx, pair[index], objects); err != nil {
			return err
		}
	}
	return nil
}
