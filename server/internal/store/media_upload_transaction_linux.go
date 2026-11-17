//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"

	"homebox/internal/mediaimage"
)

var errPrepareMediaUpload = errors.New("media upload requires image preparation")

// validateDomainResult protects frozen upload receipts, including replays after
// the original Media or files have changed. Streams are not exposed yet.
func (value Media) validateDomainResult() error {
	if !validDomainID(value.ID) || value.Revision < 1 || value.Revision > maxSafeInteger ||
		!validMediaDigest(value.SHA256) || !supportedMediaMIME(value.MIMEType) || value.Verification != "verified" ||
		value.Width < 1 || value.Height < 1 || value.Width > mediaimage.MaxDimension || value.Height > mediaimage.MaxDimension ||
		value.Width*value.Height > mediaimage.MaxPixels || value.Bytes < 1 || value.Bytes > maxMediaFileBytes ||
		value.ThumbURL != nil || value.ThumbSHA256 != nil {
		return ErrDomainState
	}
	return nil
}

// uploadStagedMedia is a private transaction, not an HTTP route or a complete
// storage service. The caller must hold bounded admission, own raw unchanged,
// and arrange reference-aware recovery for stages and promoted objects.
func (library *MediaLibrary) uploadStagedMedia(ctx context.Context, token, key, mime string, raw []byte, stagingDir, objectDir string) (Media, error) {
	return library.uploadStagedMediaGuarded(ctx, token, key, mime, raw, stagingDir, objectDir, nil)
}

func (library *MediaLibrary) uploadStagedMediaGuarded(ctx context.Context, token, key, mime string, raw []byte, stagingDir, objectDir string, guard func(queryer) error) (result Media, finalErr error) {
	if library == nil || library.auth == nil || library.auth.store == nil {
		return Media{}, ErrDomainInput
	}
	var databaseCheck func() error
	if guard != nil {
		databaseCheck = library.auth.store.requireMediaDatabaseIdentity
		if err := databaseCheck(); err != nil {
			return Media{}, err
		}
	}
	fingerprint, err := library.auth.mediaUploadFingerprint(ctx, key, mime, raw)
	if err != nil {
		return Media{}, err
	}
	request := mediaUploadReceiptRequest{RawFileMAC: fingerprint}
	// An original-key replay must precede current image policy and file reads.
	replay, err := runDomainMutationWithFinalCheck(ctx, library.auth, token, key, "post_media", request, func(*mutationTx) (Media, error) {
		return Media{}, errPrepareMediaUpload
	}, databaseCheck)
	if err == nil {
		return replay, nil
	}
	if !errors.Is(err, errPrepareMediaUpload) {
		return Media{}, err
	}
	if guard != nil {
		if err = guard(library.auth.store.db); err != nil {
			return Media{}, err
		}
	}

	preserveStages := false
	original, err := newMediaStage(ctx, stagingDir, maxMediaStageBytes)
	if err != nil {
		return Media{}, err
	}
	defer func() {
		var cleanup error
		if preserveStages {
			cleanup = original.preserve()
		} else {
			cleanup = original.discard()
		}
		finalErr = errors.Join(finalErr, cleanup)
		if finalErr != nil {
			result = Media{}
		}
	}()
	thumbnail, err := newMediaStage(ctx, stagingDir, maxMediaStageBytes)
	if err != nil {
		return Media{}, err
	}
	defer func() {
		var cleanup error
		if preserveStages {
			cleanup = thumbnail.preserve()
		} else {
			cleanup = thumbnail.discard()
		}
		finalErr = errors.Join(finalErr, cleanup)
		if finalErr != nil {
			result = Media{}
		}
	}()

	image, preview, err := mediaimage.SanitizeWithThumbnail(ctx, raw, mime, original, thumbnail, mediaimage.Limits{})
	if err != nil {
		return Media{}, err
	}
	if image.MIMEType != mime || preview.MIMEType != "image/jpeg" {
		return Media{}, ErrDomainState
	}
	originalDigest, err := original.seal()
	if err != nil {
		return Media{}, err
	}
	thumbnailDigest, err := thumbnail.seal()
	if err != nil {
		return Media{}, err
	}
	if originalDigest != (mediaStageDigest{SHA256: image.SHA256, Bytes: image.Bytes}) ||
		thumbnailDigest != (mediaStageDigest{SHA256: preview.SHA256, Bytes: preview.Bytes}) {
		return Media{}, ErrDomainState
	}
	objects, err := openPrivateMediaDirectory(objectDir)
	if err != nil {
		return Media{}, err
	}
	defer func() {
		finalErr = errors.Join(finalErr, objects.Close())
		if finalErr != nil {
			result = Media{}
		}
	}()
	pending, err := library.journalMediaObjects(ctx, token, []*mediaStage{original, thumbnail}, objects)
	if len(pending) != 0 {
		// A COMMIT error may be ambiguous, so preserve these stages even on error.
		preserveStages = true
	}
	if err != nil {
		return Media{}, err
	}
	// Keep these inode-identifiable stages until their journal is consumed by a
	// committed publication or a future reference-aware recovery process.
	if err = requireMediaDirectoryBinding(objectDir, objects); err != nil {
		return Media{}, err
	}
	promotedOriginal, err := original.promoteTo(objects)
	if err != nil {
		return Media{}, err
	}
	promotedThumbnail, err := thumbnail.promoteTo(objects)
	if err != nil {
		return Media{}, err
	}
	if err = verifyMediaObject(ctx, objects, promotedOriginal.key, originalDigest); err != nil {
		return Media{}, err
	}
	if err = verifyMediaObject(ctx, objects, promotedThumbnail.key, thumbnailDigest); err != nil {
		return Media{}, err
	}
	consumed := false
	var publicationTx *sql.Tx
	var publicationCheck func() error
	if guard != nil {
		publicationCheck = func() error {
			if publicationTx == nil {
				return databaseCheck()
			}
			return guard(publicationTx)
		}
	}
	result, err = runDomainMutationWithFinalCheck(ctx, library.auth, token, key, "post_media", request, func(m *mutationTx) (Media, error) {
		value, err := m.publishStagedMedia(image, preview, promotedOriginal, promotedThumbnail, objects, objectDir)
		if err != nil {
			return Media{}, err
		}
		if guard != nil {
			publicationTx = m.tx
		}
		if err = m.consumeMediaPending(pending); err != nil {
			return Media{}, err
		}
		consumed = true
		return value, nil
	}, publicationCheck)
	if err == nil && consumed {
		preserveStages = false
	}
	return result, err
}

func (m *mutationTx) publishStagedMedia(image, preview mediaimage.Result, original, thumbnail promotedMedia, objects *os.File, objectDir string) (Media, error) {
	if objects == nil || (image.MIMEType != "image/jpeg" && image.MIMEType != "image/png") || preview.MIMEType != "image/jpeg" ||
		image.Width < 1 || image.Height < 1 || image.Width > mediaimage.MaxDimension || image.Height > mediaimage.MaxDimension ||
		int64(image.Width)*int64(image.Height) > mediaimage.MaxPixels || image.Bytes < 1 || image.Bytes > maxMediaFileBytes ||
		preview.Width < 1 || preview.Height < 1 || preview.Width > mediaimage.MaxThumbnailDimension || preview.Height > mediaimage.MaxThumbnailDimension ||
		preview.Width > image.Width || preview.Height > image.Height || preview.Bytes < 1 || preview.Bytes > maxMediaFileBytes ||
		original.key != mediaObjectKey(image.SHA256) || original.digest != (mediaStageDigest{SHA256: image.SHA256, Bytes: image.Bytes}) ||
		thumbnail.key != mediaObjectKey(preview.SHA256) || thumbnail.digest != (mediaStageDigest{SHA256: preview.SHA256, Bytes: preview.Bytes}) {
		return Media{}, ErrDomainState
	}
	imageSHA := hex.EncodeToString(image.SHA256[:])
	thumbnailSHA := hex.EncodeToString(preview.SHA256[:])
	var id string
	err := m.tx.QueryRowContext(m.ctx, "SELECT id FROM media WHERE storage_key=?", original.key).Scan(&id)
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return Media{}, err
	}
	var want retainedMedia
	if created {
		id, err = newDomainID()
		if err != nil {
			return Media{}, err
		}
		want = retainedMedia{
			mediaData:  mediaData{ID: id, SHA256: imageSHA, MIMEType: image.MIMEType, Width: int64(image.Width), Height: int64(image.Height), Bytes: image.Bytes},
			storageKey: original.key, createdAt: stamp(m.now), Verification: "verified", Revision: 1,
			thumbnail: &retainedMediaThumbnail{SHA256: thumbnailSHA, Width: int64(preview.Width), Height: int64(preview.Height), Bytes: preview.Bytes},
		}
		err = requireDomainRow(m.tx.ExecContext(m.ctx, `INSERT INTO media(id,storage_key,sha256,mime_type,width,height,bytes,created_at,verification)
 VALUES(?,?,?,?,?,?,?,?,'verified')`, id, want.storageKey, want.SHA256, want.MIMEType, want.Width, want.Height, want.Bytes, want.createdAt))
		if err != nil {
			return Media{}, err
		}
		err = requireDomainRow(m.tx.ExecContext(m.ctx, `INSERT INTO media_thumbnails(media_id,sha256,width,height,bytes) VALUES(?,?,?,?,?)`,
			id, want.thumbnail.SHA256, want.thumbnail.Width, want.thumbnail.Height, want.thumbnail.Bytes))
		if err != nil {
			return Media{}, err
		}
		if revision, err := m.touch("media", id, false); err != nil {
			return Media{}, err
		} else if revision != 1 {
			return Media{}, ErrDomainState
		}
	} else {
		want, err = loadMedia(m.ctx, m.tx, id)
		if err != nil {
			return Media{}, mediaAuthorityError(err)
		}
		// A retained claim without the exact independently generated thumbnail
		// is not eligible for automatic adoption or replacement.
		if want.Verification != "verified" || want.storageKey != original.key || want.SHA256 != imageSHA || want.MIMEType != image.MIMEType ||
			want.Width != int64(image.Width) || want.Height != int64(image.Height) || want.Bytes != image.Bytes || want.thumbnail == nil ||
			*want.thumbnail != (retainedMediaThumbnail{SHA256: thumbnailSHA, Width: int64(preview.Width), Height: int64(preview.Height), Bytes: preview.Bytes}) {
			return Media{}, ErrDomainState
		}
		if err = requireCurrentMediaPayload(m.ctx, m.tx, want); err != nil {
			return Media{}, err
		}
	}
	members, err := readMediaMembers(m.ctx, m.tx, id)
	if err != nil {
		return Media{}, err
	}
	if created && len(members) != 0 {
		return Media{}, ErrDomainState
	}
	m.finalChecks = append(m.finalChecks, func() error {
		actual, err := loadMedia(m.ctx, m.tx, id)
		if err != nil {
			return mediaAuthorityError(err)
		}
		if !reflect.DeepEqual(actual, want) {
			return ErrDomainState
		}
		currentMembers, err := readMediaMembers(m.ctx, m.tx, id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(currentMembers, members) {
			return ErrDomainState
		}
		if created {
			err = m.requirePublishedMedia("media", id, want.Revision, want.mediaData)
		} else {
			err = requireCurrentMediaPayload(m.ctx, m.tx, want)
		}
		if err != nil {
			return err
		}
		if err = verifyMediaObject(m.ctx, objects, original.key, original.digest); err != nil {
			return err
		}
		if err = verifyMediaObject(m.ctx, objects, thumbnail.key, thumbnail.digest); err != nil {
			return err
		}
		return requireMediaDirectoryBinding(objectDir, objects)
	})
	return publicMedia(want), nil
}

func requireCurrentMediaPayload(ctx context.Context, tx *sql.Tx, value retainedMedia) error {
	expected, err := json.Marshal(value.mediaData)
	if err != nil {
		return err
	}
	var payload string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM current_projections WHERE entity_type='media' AND entity_id=? AND entity_revision=?`, value.ID, value.Revision).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) || err == nil && payload != string(expected) {
		return ErrDomainState
	}
	return err
}
