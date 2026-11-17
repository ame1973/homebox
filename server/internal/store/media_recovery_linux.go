//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var errMediaRecoveryUnproven = errors.New("pending media requires manual review")

type mediaRecoveryResult struct {
	StageRemoved  bool
	ObjectRemoved bool
	Referenced    bool
}

type mediaRecoveryScope struct {
	referenced bool
	sourceHash [sha256.Size]byte
}

type mediaRecoverySyncState struct {
	Instance          string
	Epoch             string
	ProjectionVersion int64
	MinRetainedSeq    int64
	UpdatedAt         string
}

func loadMediaRecoverySyncState(ctx context.Context, tx *sql.Tx) (mediaRecoverySyncState, error) {
	var value mediaRecoverySyncState
	err := tx.QueryRowContext(ctx, `SELECT server_instance_id,dataset_epoch,projection_version,min_retained_seq,updated_at
 FROM sync_state WHERE singleton=1`).Scan(&value.Instance, &value.Epoch, &value.ProjectionVersion, &value.MinRetainedSeq, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!validMediaInstance(value.Instance) || !authString(value.Epoch, 1, 128) ||
		value.ProjectionVersion != 1 || value.MinRetainedSeq < 0 || !authString(value.UpdatedAt, 0, 10000)) {
		return mediaRecoverySyncState{}, ErrDomainState
	}
	return value, err
}

func hashRecoveryRecord(hasher hash.Hash, kind byte, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var prefix [9]byte
	prefix[0] = kind
	binary.BigEndian.PutUint64(prefix[1:], uint64(len(encoded)))
	_, _ = hasher.Write(prefix[:])
	_, _ = hasher.Write(encoded)
	return nil
}

// mediaRecoveryReferences scans actual source rather than projections. An
// opaque legacy key could alias an object, so no file is removed until that
// source has been explicitly reconciled. Orphan thumbnails also fail closed.
// ponytail: scan the household's Media for each recovery; batch if profiling
// shows that a large pending backlog makes startup materially slower.
func mediaRecoveryReferences(ctx context.Context, tx *sql.Tx, pending pendingMediaObject) (mediaRecoveryScope, error) {
	var scope mediaRecoveryScope
	rows, err := tx.QueryContext(ctx, mediaSelect+" ORDER BY m.id")
	if err != nil {
		return scope, err
	}
	hash := sha256.New()
	for rows.Next() {
		value, scanErr := scanMedia(rows)
		if scanErr != nil {
			return mediaRecoveryScope{}, errors.Join(errMediaRecoveryUnproven, scanErr, rows.Close())
		}
		if value.storageKey != "sha256-"+value.SHA256 || !authString(value.createdAt, 0, 10000) {
			return mediaRecoveryScope{}, errors.Join(errMediaRecoveryUnproven, rows.Close())
		}
		if value.SHA256 == pending.SHA256 || value.storageKey == "sha256-"+pending.SHA256 ||
			value.thumbnail != nil && value.thumbnail.SHA256 == pending.SHA256 {
			scope.referenced = true
		}
		source := struct {
			Data         mediaData
			StorageKey   string
			CreatedAt    string
			Verification string
			Revision     int64
			Thumbnail    *retainedMediaThumbnail
		}{value.mediaData, value.storageKey, value.createdAt, value.Verification, value.Revision, value.thumbnail}
		if encodeErr := hashRecoveryRecord(hash, 's', source); encodeErr != nil {
			return mediaRecoveryScope{}, errors.Join(encodeErr, rows.Close())
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return mediaRecoveryScope{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT p.entity_id,p.entity_revision,p.projection_version,p.payload_json,p.updated_at,COALESCE(r.revision,0)
 FROM current_projections p LEFT JOIN projection_revisions r ON r.entity_type=p.entity_type AND r.entity_id=p.entity_id
 WHERE p.entity_type='media' ORDER BY p.entity_id`)
	if err != nil {
		return mediaRecoveryScope{}, err
	}
	for rows.Next() {
		var value struct {
			ID              string
			Revision        int64
			Version         int64
			Payload         string
			UpdatedAt       string
			CounterRevision int64
		}
		if err = rows.Scan(&value.ID, &value.Revision, &value.Version, &value.Payload, &value.UpdatedAt, &value.CounterRevision); err != nil {
			return mediaRecoveryScope{}, errors.Join(err, rows.Close())
		}
		payload := strings.TrimSpace(value.Payload)
		if !validProjectionIdentity("media", value.ID) || value.Revision < 1 || value.Revision > maxSafeInteger ||
			value.CounterRevision != value.Revision || value.Version != 1 || len(value.Payload) > 128<<10 || len(payload) == 0 || payload[0] != '{' ||
			!utf8.ValidString(value.Payload) || !json.Valid([]byte(value.Payload)) || !authString(value.UpdatedAt, 0, 10000) {
			return mediaRecoveryScope{}, errors.Join(errMediaRecoveryUnproven, rows.Close())
		}
		if err = hashRecoveryRecord(hash, 'p', value); err != nil {
			return mediaRecoveryScope{}, errors.Join(err, rows.Close())
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return mediaRecoveryScope{}, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT entity_id,revision FROM projection_revisions WHERE entity_type='media' ORDER BY entity_id")
	if err != nil {
		return mediaRecoveryScope{}, err
	}
	for rows.Next() {
		var value struct {
			ID       string
			Revision int64
		}
		if err = rows.Scan(&value.ID, &value.Revision); err != nil {
			return mediaRecoveryScope{}, errors.Join(err, rows.Close())
		}
		if !validProjectionIdentity("media", value.ID) || value.Revision < 1 || value.Revision > maxSafeInteger {
			return mediaRecoveryScope{}, errors.Join(errMediaRecoveryUnproven, rows.Close())
		}
		if err = hashRecoveryRecord(hash, 'r', value); err != nil {
			return mediaRecoveryScope{}, errors.Join(err, rows.Close())
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return mediaRecoveryScope{}, err
	}
	var orphan bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media_thumbnails t
 LEFT JOIN media m ON m.id=t.media_id WHERE m.id IS NULL)`).Scan(&orphan)
	if err != nil {
		return mediaRecoveryScope{}, err
	}
	if orphan {
		return mediaRecoveryScope{}, errMediaRecoveryUnproven
	}
	copy(scope.sourceHash[:], hash.Sum(nil))
	return scope, nil
}

func recoveryOtherPendingHash(ctx context.Context, tx *sql.Tx, selected string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	rows, err := tx.QueryContext(ctx, mediaPendingSelect+" WHERE id<>? ORDER BY id", selected)
	if err != nil {
		return result, err
	}
	hasher := sha256.New()
	for rows.Next() {
		value, scanErr := scanMediaPendingRow(rows)
		if scanErr != nil || !validPendingMediaObject(value) {
			return result, errors.Join(errMediaRecoveryUnproven, scanErr, rows.Close())
		}
		if err = hashRecoveryRecord(hasher, 'j', value); err != nil {
			return result, errors.Join(err, rows.Close())
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	copy(result[:], hasher.Sum(nil))
	return result, nil
}

func recoveryDigest(value pendingMediaObject) (mediaStageDigest, error) {
	var digest mediaStageDigest
	if !validPendingMediaObject(value) {
		return digest, ErrDomainState
	}
	if _, err := hex.Decode(digest.SHA256[:], []byte(value.SHA256)); err != nil {
		return mediaStageDigest{}, ErrDomainState
	}
	digest.Bytes = value.Bytes
	return digest, nil
}

// openRecoveryStage accepts only the journaled regular inode and actual bytes.
// A writable stage cannot have another hard link, including an object link.
func openRecoveryStage(ctx context.Context, dir *os.File, pending pendingMediaObject, expected mediaStageDigest) (_ *os.File, _ unix.Stat_t, finalErr error) {
	var empty unix.Stat_t
	fd, err := unix.Openat(int(dir.Fd()), pending.StageName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, empty, nil
	}
	if err != nil {
		return nil, empty, errors.Join(errMediaRecoveryUnproven, err)
	}
	file := os.NewFile(uintptr(fd), "media-recovery-stage")
	defer func() {
		if finalErr != nil {
			finalErr = errors.Join(finalErr, file.Close())
		}
	}()
	var opened, named unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil {
		return nil, empty, err
	}
	if err = unix.Fstatat(int(dir.Fd()), pending.StageName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, empty, errors.Join(errMediaRecoveryUnproven, err)
	}
	if !sameMediaFile(opened, named) || uint64(opened.Dev) != uint64(pending.StageDevice) || opened.Ino != uint64(pending.StageInode) ||
		opened.Size != expected.Bytes || opened.Mode&0o7777 != 0o400 && opened.Mode&0o7777 != 0o600 ||
		opened.Nlink < 1 || opened.Nlink > 2 || opened.Mode&0o7777 == 0o600 && opened.Nlink != 1 {
		return nil, empty, errMediaRecoveryUnproven
	}
	actual, err := hashMediaFile(ctx, file, expected.Bytes)
	if err != nil {
		return nil, empty, errors.Join(errMediaRecoveryUnproven, err)
	}
	if actual != expected.SHA256 {
		return nil, empty, errMediaRecoveryUnproven
	}
	var after unix.Stat_t
	if err = unix.Fstat(fd, &after); err != nil {
		return nil, empty, err
	}
	if err = unix.Fstatat(int(dir.Fd()), pending.StageName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, empty, errors.Join(errMediaRecoveryUnproven, err)
	}
	if !sameMediaFile(opened, after) || !sameMediaFile(opened, named) || after.Mode != opened.Mode || after.Size != opened.Size || after.Nlink != opened.Nlink {
		return nil, empty, errMediaRecoveryUnproven
	}
	return file, after, nil
}

func recoveryObject(ctx context.Context, dir *os.File, expected mediaStageDigest) (unix.Stat_t, bool, error) {
	var before, after unix.Stat_t
	key := mediaObjectKey(expected.SHA256)
	err := unix.Fstatat(int(dir.Fd()), key, &before, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return after, false, nil
	}
	if err != nil {
		return after, false, errors.Join(errMediaRecoveryUnproven, err)
	}
	if err = inspectMediaObject(ctx, dir, key, expected, true); err != nil {
		return after, false, errors.Join(errMediaRecoveryUnproven, err)
	}
	if err = unix.Fstatat(int(dir.Fd()), key, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return after, false, errors.Join(errMediaRecoveryUnproven, err)
	}
	if !sameMediaFile(before, after) || before.Mode != after.Mode || before.Size != after.Size || before.Nlink != after.Nlink {
		return after, false, errMediaRecoveryUnproven
	}
	return after, true, nil
}

func syncRecoveryDirectory(dir *os.File) error {
	return errors.Join(dir.Sync(), syncMediaStageParent(dir))
}

func requireRecoveryName(dir *os.File, name string, expected *unix.Stat_t) error {
	var actual unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &actual, unix.AT_SYMLINK_NOFOLLOW)
	if expected == nil && errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errors.Join(errMediaRecoveryUnproven, err)
	}
	if expected == nil || !sameMediaFile(*expected, actual) || actual.Mode != expected.Mode || actual.Size != expected.Size || actual.Nlink != expected.Nlink {
		return errMediaRecoveryUnproven
	}
	return nil
}

// pendingMediaRecoveryPage enumerates exact journal identities for a future
// recovery coordinator. It is a live internal page, not a sync snapshot.
func (storage *mediaStorage) pendingMediaRecoveryPage(ctx context.Context, after string, limit int) ([]string, string, error) {
	if storage == nil || storage.store == nil || storage.store.db == nil || ctx == nil || limit < 1 || limit > 100 || after != "" && !validDomainID(after) {
		return nil, "", ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.pendingMediaRecoveryPageLocked(ctx, after, limit)
}

func (storage *mediaStorage) pendingMediaRecoveryPageLocked(ctx context.Context, after string, limit int) ([]string, string, error) {
	if storage.closed {
		return nil, "", errMediaStorageClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	tx, err := storage.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if err = storage.validateLocked(ctx, tx); err != nil {
		return nil, "", err
	}
	query := mediaPendingSelect + " ORDER BY id LIMIT ?"
	args := []any{limit + 1}
	if after != "" {
		query = mediaPendingSelect + " WHERE id>? ORDER BY id LIMIT ?"
		args = []any{after, limit + 1}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, limit)
	previous := after
	more := false
	for rows.Next() {
		value, scanErr := scanMediaPendingRow(rows)
		if scanErr != nil || !validPendingMediaObject(value) || value.ID <= previous {
			return nil, "", errors.Join(ErrDomainState, scanErr, rows.Close())
		}
		previous = value.ID
		if len(ids) == limit {
			more = true
			break
		}
		ids = append(ids, value.ID)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, "", err
	}
	if err = storage.validateLocked(ctx, tx); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	if more {
		return ids, ids[len(ids)-1], nil
	}
	return ids, "", nil
}

// recoverPendingMediaBatch processes one bounded live page under the same owner
// lock. Each object commits independently; on error completed counts only prior
// acknowledged objects, while the failed attempt may already have changed files.
// The more flag is meaningful only on success; callers must explicitly retry
// rather than skipping the unresolved identity.
func (storage *mediaStorage) recoverPendingMediaBatch(ctx context.Context, limit int) (completed int, more bool, finalErr error) {
	if storage == nil || storage.store == nil || storage.store.db == nil || ctx == nil || limit < 1 || limit > 100 {
		return 0, false, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	ids, next, err := storage.pendingMediaRecoveryPageLocked(ctx, "", limit)
	if err != nil {
		return 0, false, err
	}
	for _, id := range ids {
		if _, err = storage.recoverPendingMediaLocked(ctx, id); err != nil {
			return completed, false, err
		}
		completed++
	}
	return completed, next != "", nil
}

// recoverPendingMedia handles one journaled object under exclusive storage and
// SQLite ownership. Errors may follow a durable unlink; the original row is
// retained when possible and the caller must explicitly retry or inspect it.
// It is not an HTTP route, unjournaled-file sweep, restore or power-loss proof.
func (storage *mediaStorage) recoverPendingMedia(ctx context.Context, id string) (result mediaRecoveryResult, finalErr error) {
	if storage == nil || storage.store == nil || storage.store.db == nil || ctx == nil || !validDomainID(id) {
		return result, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	storage.mu.Lock()
	defer storage.mu.Unlock()
	return storage.recoverPendingMediaLocked(ctx, id)
}

func (storage *mediaStorage) recoverPendingMediaLocked(ctx context.Context, id string) (result mediaRecoveryResult, finalErr error) {
	if storage.closed {
		return result, errMediaStorageClosed
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	tx, err := storage.store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = storage.validateLocked(ctx, tx); err != nil {
		return result, err
	}
	pending, err := loadMediaPendingRow(ctx, tx, id)
	if err != nil {
		return result, err
	}
	expected, err := recoveryDigest(pending)
	if err != nil {
		return result, err
	}
	if err = requireMediaPendingEpoch(ctx, tx, pending.Epoch); err != nil {
		return result, err
	}
	syncState, err := loadMediaRecoverySyncState(ctx, tx)
	if err != nil {
		return result, err
	}
	if syncState.Epoch != pending.Epoch || syncState.Instance != storage.instance {
		return result, ErrDomainState
	}
	staging, err := mediaDirectoryID(storage.staging)
	if err != nil {
		return result, err
	}
	objects, err := mediaDirectoryID(storage.objects)
	if err != nil {
		return result, err
	}
	if staging != (mediaDirectoryIdentity{device: pending.StagingDevice, inode: pending.StagingInode}) ||
		objects != (mediaDirectoryIdentity{device: pending.ObjectDevice, inode: pending.ObjectInode}) {
		return result, errMediaRecoveryUnproven
	}
	otherPending, err := recoveryOtherPendingHash(ctx, tx, pending.ID)
	if err != nil {
		return result, err
	}
	scope, err := mediaRecoveryReferences(ctx, tx, pending)
	if err != nil {
		return result, err
	}
	referenced := scope.referenced
	stage, stageInfo, err := openRecoveryStage(ctx, storage.staging, pending, expected)
	if err != nil {
		return result, err
	}
	if stage != nil {
		defer func() {
			finalErr = errors.Join(finalErr, stage.Close())
			if finalErr != nil {
				result = mediaRecoveryResult{}
			}
		}()
	}
	objectInfo, exists, err := recoveryObject(ctx, storage.objects, expected)
	if err != nil {
		return result, err
	}
	if stage == nil && exists && !referenced || referenced && !exists {
		return result, errMediaRecoveryUnproven
	}
	linked := stage != nil && exists && sameMediaFile(stageInfo, objectInfo)
	if stage != nil && (linked && stageInfo.Nlink != 2 || !linked && stageInfo.Nlink != 1) {
		return result, errMediaRecoveryUnproven
	}
	if err = storage.validateLocked(ctx, tx); err != nil {
		return result, err
	}
	if err = requireMediaPendingRow(ctx, tx, pending); err != nil {
		return result, err
	}
	key := mediaObjectKey(expected.SHA256)
	removedObject := false
	if linked && !referenced {
		if err = requireRecoveryName(storage.staging, pending.StageName, &stageInfo); err != nil {
			return result, err
		}
		if err = requireRecoveryName(storage.objects, key, &objectInfo); err != nil {
			return result, err
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		unlinkErr := unix.Unlinkat(int(storage.objects.Fd()), key, 0)
		// An interrupted unlink can be ambiguous. Synchronize even on an error
		// or cancellation before deciding whether a retry is necessary.
		syncErr := syncRecoveryDirectory(storage.objects)
		if unlinkErr != nil || syncErr != nil {
			return result, errors.Join(unlinkErr, syncErr, ctx.Err())
		}
		removedObject = true
		stageInfo.Nlink--
	} else if exists {
		if err = syncRecoveryDirectory(storage.objects); err != nil {
			return result, errors.Join(err, ctx.Err())
		}
	}
	removedStage := false
	if stage != nil {
		if err = storage.validateLocked(ctx, tx); err != nil {
			return result, err
		}
		if err = requireRecoveryName(storage.staging, pending.StageName, &stageInfo); err != nil {
			return result, err
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		unlinkErr := unix.Unlinkat(int(storage.staging.Fd()), pending.StageName, 0)
		syncErr := syncRecoveryDirectory(storage.staging)
		if unlinkErr != nil || syncErr != nil {
			return result, errors.Join(unlinkErr, syncErr, ctx.Err())
		}
		removedStage = true
	} else if err = syncRecoveryDirectory(storage.staging); err != nil {
		return result, errors.Join(err, ctx.Err())
	}
	if !exists {
		if err = syncRecoveryDirectory(storage.objects); err != nil {
			return result, errors.Join(err, ctx.Err())
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = requireDomainRow(tx.ExecContext(ctx, "DELETE FROM media_pending_objects WHERE id=?", pending.ID)); err != nil {
		return result, err
	}
	if err = requireMediaPendingEpoch(ctx, tx, pending.Epoch); err != nil {
		return result, err
	}
	currentSyncState, err := loadMediaRecoverySyncState(ctx, tx)
	if err != nil {
		return result, err
	}
	if currentSyncState != syncState {
		return result, ErrDomainState
	}
	var stillPending bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM media_pending_objects WHERE id=?)", pending.ID).Scan(&stillPending); err != nil {
		return result, err
	}
	if stillPending {
		return result, ErrDomainState
	}
	currentPending, err := recoveryOtherPendingHash(ctx, tx, pending.ID)
	if err != nil {
		return result, err
	}
	if currentPending != otherPending {
		return result, ErrDomainState
	}
	currentScope, err := mediaRecoveryReferences(ctx, tx, pending)
	if err != nil {
		return result, err
	}
	if currentScope != scope {
		return result, ErrDomainState
	}
	if err = storage.validateLocked(ctx, tx); err != nil {
		return result, err
	}
	if err = requireRecoveryName(storage.staging, pending.StageName, nil); err != nil {
		return result, err
	}
	if !exists || removedObject {
		if err = requireRecoveryName(storage.objects, key, nil); err != nil {
			return result, err
		}
	} else {
		if _, current, verifyErr := recoveryObject(ctx, storage.objects, expected); verifyErr != nil || !current {
			return result, errors.Join(errMediaRecoveryUnproven, verifyErr)
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return mediaRecoveryResult{StageRemoved: removedStage, ObjectRemoved: removedObject, Referenced: referenced}, nil
}
