//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

// pendingMediaObject identifies one sanitized object before filesystem promotion.
// It contains no raw upload bytes, filenames, session credentials or request key.
type pendingMediaObject struct {
	ID, Epoch, SHA256, CreatedAt, StageName                     string
	Bytes, StageDevice, StageInode, StagingDevice, StagingInode int64
	ObjectDevice, ObjectInode                                   int64
}

type mediaDirectoryIdentity struct{ device, inode int64 }

func mediaDirectoryID(dir *os.File) (mediaDirectoryIdentity, error) {
	if dir == nil {
		return mediaDirectoryIdentity{}, ErrUnsafePath
	}
	if err := checkPrivateMediaDirectory(dir); err != nil {
		return mediaDirectoryIdentity{}, err
	}
	var info unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &info); err != nil {
		return mediaDirectoryIdentity{}, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(info.Dev) > math.MaxInt64 || info.Ino == 0 || info.Ino > math.MaxInt64 {
		return mediaDirectoryIdentity{}, ErrUnsafePath
	}
	return mediaDirectoryIdentity{device: int64(info.Dev), inode: int64(info.Ino)}, nil
}

func requireMediaDirectoryBinding(path string, anchored *os.File) (finalErr error) {
	expected, err := mediaDirectoryID(anchored)
	if err != nil {
		return err
	}
	current, err := openPrivateMediaDirectory(path)
	if err != nil {
		return err
	}
	defer func() { finalErr = errors.Join(finalErr, current.Close()) }()
	actual, err := mediaDirectoryID(current)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrUnsafePath
	}
	return nil
}

func journalStage(stage *mediaStage, objects mediaDirectoryIdentity) (pendingMediaObject, error) {
	if stage == nil {
		return pendingMediaObject{}, ErrDomainInput
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if err := stage.checkPromotion(); err != nil {
		return pendingMediaObject{}, err
	}
	staging, err := mediaDirectoryID(stage.dir)
	if err != nil {
		return pendingMediaObject{}, err
	}
	var source unix.Stat_t
	if err = unix.Fstatat(int(stage.dir.Fd()), stage.name, &source, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return pendingMediaObject{}, err
	}
	if source.Mode&unix.S_IFMT != unix.S_IFREG || source.Mode&0o7777 != 0o600 || source.Nlink != 1 ||
		uint64(source.Dev) != stage.device || source.Ino != stage.inode {
		return pendingMediaObject{}, ErrUnsafePath
	}
	if !validMediaStageName(stage.name) || stage.device > math.MaxInt64 || stage.inode == 0 || stage.inode > math.MaxInt64 ||
		int64(stage.device) != staging.device || stage.result.Bytes < 1 || stage.result.Bytes > maxMediaFileBytes {
		return pendingMediaObject{}, ErrUnsafePath
	}
	if staging == objects {
		return pendingMediaObject{}, errMediaObjectDirectory
	}
	if staging.device != objects.device {
		return pendingMediaObject{}, unix.EXDEV
	}
	return pendingMediaObject{SHA256: hex.EncodeToString(stage.result.SHA256[:]), Bytes: stage.result.Bytes,
		StageName: stage.name, StageDevice: int64(stage.device), StageInode: int64(stage.inode),
		StagingDevice: staging.device, StagingInode: staging.inode, ObjectDevice: objects.device, ObjectInode: objects.inode}, nil
}

func (library *MediaLibrary) journalMediaObjects(ctx context.Context, token string, stages []*mediaStage, objects *os.File) ([]pendingMediaObject, error) {
	if library == nil || library.auth == nil || library.auth.store == nil || ctx == nil || len(stages) != 2 || stages[0] == stages[1] {
		return nil, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objectID, err := mediaDirectoryID(objects)
	if err != nil {
		return nil, err
	}
	pending := make([]pendingMediaObject, 0, len(stages))
	for _, stage := range stages {
		value, err := journalStage(stage, objectID)
		if err != nil {
			return nil, err
		}
		pending = append(pending, value)
	}
	if pending[0].StageDevice == pending[1].StageDevice && pending[0].StageInode == pending[1].StageInode ||
		pending[0].StagingDevice != pending[1].StagingDevice || pending[0].StagingInode != pending[1].StagingInode {
		return nil, ErrDomainInput
	}
	tx, err := library.auth.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := library.auth.now().UTC()
	session, err := library.auth.loadSession(ctx, tx, token, now)
	if err != nil {
		return nil, err
	}
	if err = requireCompleteSession(session); err != nil {
		return nil, err
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, "SELECT dataset_epoch FROM sync_state WHERE singleton=1").Scan(&epoch); err != nil {
		return nil, err
	}
	if !authString(epoch, 1, 128) {
		return nil, ErrDomainState
	}
	for index := range pending {
		id, err := newDomainID()
		if err != nil {
			return nil, err
		}
		pending[index].ID, pending[index].Epoch, pending[index].CreatedAt = id, epoch, stamp(now)
		value := pending[index]
		err = requireDomainRow(tx.ExecContext(ctx, `INSERT INTO media_pending_objects(id,dataset_epoch,sha256,bytes,created_at,stage_name,stage_device,stage_inode,staging_device,staging_inode,object_device,object_inode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			value.ID, value.Epoch, value.SHA256, value.Bytes, value.CreatedAt, value.StageName, value.StageDevice, value.StageInode,
			value.StagingDevice, value.StagingInode, value.ObjectDevice, value.ObjectInode))
		if err != nil {
			return nil, err
		}
	}
	if err = finishDomainSession(ctx, library.auth, tx, token, session); err != nil {
		return nil, err
	}
	if err = requireMediaPendingEpoch(ctx, tx, epoch); err != nil {
		return nil, err
	}
	for index, value := range pending {
		if err = requireMediaPendingRow(ctx, tx, value); err != nil {
			return nil, err
		}
		actual, err := journalStage(stages[index], objectID)
		if err != nil {
			return nil, err
		}
		actual.ID, actual.Epoch, actual.CreatedAt = value.ID, value.Epoch, value.CreatedAt
		if actual != value {
			return nil, ErrDomainState
		}
	}
	if current, err := mediaDirectoryID(objects); err != nil {
		return nil, err
	} else if current != objectID {
		return nil, ErrDomainState
	}
	if _, err = checkDomainSession(ctx, library.auth, tx, token, session, library.auth.now().UTC()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		// A failed COMMIT can be ambiguous. Return the identities with the error
		// so the caller preserves the stages for conservative recovery.
		return pending, err
	}
	return pending, nil
}

func requireMediaPendingEpoch(ctx context.Context, tx *sql.Tx, epoch string) error {
	var current string
	err := tx.QueryRowContext(ctx, "SELECT dataset_epoch FROM sync_state WHERE singleton=1").Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || err == nil && current != epoch {
		return ErrDomainState
	}
	return err
}

func requireMediaPendingRow(ctx context.Context, tx *sql.Tx, expected pendingMediaObject) error {
	var actual pendingMediaObject
	err := tx.QueryRowContext(ctx, `SELECT id,dataset_epoch,sha256,bytes,created_at,stage_name,stage_device,stage_inode,staging_device,staging_inode,object_device,object_inode FROM media_pending_objects WHERE id=?`, expected.ID).
		Scan(&actual.ID, &actual.Epoch, &actual.SHA256, &actual.Bytes, &actual.CreatedAt, &actual.StageName, &actual.StageDevice, &actual.StageInode,
			&actual.StagingDevice, &actual.StagingInode, &actual.ObjectDevice, &actual.ObjectInode)
	if errors.Is(err, sql.ErrNoRows) || err == nil && actual != expected {
		return ErrDomainState
	}
	return err
}

// consumeMediaPending runs inside the source publication transaction. The
// retained original rows are checked before deletion and again after audit and
// session activity so a late trigger cannot silently leave a different claim.
func (m *mutationTx) consumeMediaPending(input []pendingMediaObject) error {
	if len(input) != 2 || input[0].ID == input[1].ID ||
		input[0].StageDevice == input[1].StageDevice && input[0].StageInode == input[1].StageInode ||
		input[0].StagingDevice != input[1].StagingDevice || input[0].StagingInode != input[1].StagingInode ||
		input[0].ObjectDevice != input[1].ObjectDevice || input[0].ObjectInode != input[1].ObjectInode {
		return ErrDomainState
	}
	pending := append([]pendingMediaObject(nil), input...)
	epoch := pending[0].Epoch
	if !authString(epoch, 1, 128) {
		return ErrDomainState
	}
	if err := requireMediaPendingEpoch(m.ctx, m.tx, epoch); err != nil {
		return err
	}
	for _, value := range pending {
		if !validDomainID(value.ID) || value.Epoch != epoch || !validMediaDigest(value.SHA256) ||
			value.Bytes < 1 || value.Bytes > maxMediaFileBytes || !authString(value.CreatedAt, 1, 128) || !validMediaStageName(value.StageName) ||
			value.StageDevice < 0 || value.StageInode < 1 || value.StagingDevice != value.StageDevice || value.StagingInode < 1 ||
			value.ObjectDevice != value.StageDevice || value.ObjectInode < 1 || value.ObjectInode == value.StagingInode {
			return ErrDomainState
		}
		if err := requireMediaPendingRow(m.ctx, m.tx, value); err != nil {
			return err
		}
	}
	for _, value := range pending {
		if err := requireDomainRow(m.tx.ExecContext(m.ctx, "DELETE FROM media_pending_objects WHERE id=?", value.ID)); err != nil {
			return err
		}
	}
	m.finalChecks = append(m.finalChecks, func() error {
		if err := requireMediaPendingEpoch(m.ctx, m.tx, epoch); err != nil {
			return err
		}
		for _, value := range pending {
			var exists bool
			if err := m.tx.QueryRowContext(m.ctx, "SELECT EXISTS(SELECT 1 FROM media_pending_objects WHERE id=?)", value.ID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return ErrDomainState
			}
		}
		return nil
	})
	return nil
}
