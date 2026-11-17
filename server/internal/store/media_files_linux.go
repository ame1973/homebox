//go:build linux

package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

var (
	ErrMediaFilesActive        = errors.New("managed media files are already active")
	ErrMediaFilesClosed        = errors.New("managed media files are closed")
	ErrMediaRecoveryNotBlocked = errors.New("media recovery is not awaiting retry")
)

// MediaRecoveryStatus exposes only bounded, non-sensitive coordinator state.
// Completed counts acknowledged rows in this service lifetime, not all cleanup.
// Saturated means Completed reached its representable public limit.
type MediaRecoveryStatus struct {
	Phase     string `json:"phase"`
	Completed int64  `json:"completed"`
	Saturated bool   `json:"saturated"`
	Failure   string `json:"failure"`
}

// MediaFiles owns one private storage scope and its recovery worker. A caller
// must hold bounded upload admission from before body reading until Upload
// returns. Status and retry are not authentication or HTTP authorization.
type MediaFiles struct {
	mu       sync.RWMutex
	store    *Store
	library  *MediaLibrary
	storage  *mediaStorage
	worker   *mediaRecoveryWorker
	closed   bool
	closeErr error
}

// StartMediaFiles opens a single managed storage scope. ctx bounds startup only;
// Close or Store.Close stops and waits for the independently owned worker.
func (s *Store) StartMediaFiles(ctx context.Context, auth *Auth) (*MediaFiles, error) {
	if s == nil || ctx == nil || auth == nil || auth.store != s || s.offlineAuth {
		return nil, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if s.closing {
		return nil, ErrMediaFilesClosed
	}
	if s.mediaFiles != nil {
		return nil, ErrMediaFilesActive
	}
	library, err := s.NewMediaLibrary(auth)
	if err != nil {
		return nil, err
	}
	storage, err := s.openMediaStorage(ctx)
	if err != nil {
		return nil, err
	}
	worker, err := startMediaRecoveryWorker(context.Background(), storage)
	if err != nil {
		return nil, errors.Join(err, storage.Close())
	}
	if err = ctx.Err(); err != nil {
		return nil, errors.Join(err, worker.close(), storage.Close())
	}
	files := &MediaFiles{store: s, library: library, storage: storage, worker: worker}
	s.mediaFiles = files
	return files, nil
}

// Upload validates and publishes one immutable caller-owned raw byte slice.
// Notify recovery even when publication fails or its result is uncertain. A
// blocked worker retains its error until explicit retry; uploads can still
// commit and leave additional journaled stages for that retry.
func (files *MediaFiles) Upload(ctx context.Context, token, key, mime string, raw []byte) (Media, error) {
	if files == nil || ctx == nil {
		return Media{}, ErrDomainInput
	}
	files.mu.RLock()
	defer files.mu.RUnlock()
	if files.closed || files.worker.ctx.Err() != nil {
		return Media{}, ErrMediaFilesClosed
	}
	value, err := files.storage.upload(ctx, files.library, token, key, mime, raw)
	if notifyErr := files.worker.notify(); notifyErr != nil && !errors.Is(notifyErr, errMediaRecoveryWorkerBlocked) {
		return Media{}, errors.Join(err, notifyErr)
	}
	return value, err
}

func (files *MediaFiles) RecoveryStatus() MediaRecoveryStatus {
	if files == nil {
		return MediaRecoveryStatus{Phase: string(mediaRecoveryWorkerStopped)}
	}
	files.mu.RLock()
	defer files.mu.RUnlock()
	return publicMediaRecoveryStatus(files.worker.snapshot())
}

func publicMediaRecoveryStatus(status mediaRecoveryWorkerStatus) MediaRecoveryStatus {
	return MediaRecoveryStatus{Phase: string(status.Phase), Completed: status.Completed, Saturated: status.Saturated, Failure: string(status.Failure)}
}

// ReadMediaRecoveryStatus returns a non-sensitive live snapshot only after a
// completed session is revalidated. A nil files owner reports unavailable; it
// does not authorize starting storage or retrying recovery.
func (a *Auth) ReadMediaRecoveryStatus(ctx context.Context, token string, files *MediaFiles) (MediaRecoveryStatus, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil {
		return MediaRecoveryStatus{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return MediaRecoveryStatus{}, err
	}
	if files != nil {
		// Hold the lifetime lock before entering SQLite: Close waits for the
		// worker, and the worker may itself be waiting for the DB connection.
		files.mu.RLock()
		defer files.mu.RUnlock()
		if files.store != a.store || files.library == nil || files.library.auth != a || files.worker == nil {
			return MediaRecoveryStatus{}, ErrDomainInput
		}
	}
	return readDomain(ctx, a, token, func(*sql.Tx) (MediaRecoveryStatus, error) {
		if files == nil {
			return MediaRecoveryStatus{Phase: "unavailable", Failure: string(mediaRecoveryFailureStorage)}, nil
		}
		return publicMediaRecoveryStatus(files.worker.snapshot()), nil
	})
}

// RetryRecovery requests one explicit retry after a blocked pass. The caller
// must authorize that action independently and must not assume immediate success.
func (files *MediaFiles) RetryRecovery() error {
	if files == nil {
		return ErrDomainInput
	}
	files.mu.RLock()
	defer files.mu.RUnlock()
	if files.closed {
		return ErrMediaFilesClosed
	}
	err := files.worker.retry()
	if errors.Is(err, errMediaRecoveryWorkerBusy) {
		return ErrMediaRecoveryNotBlocked
	}
	if errors.Is(err, errMediaRecoveryWorkerClosed) {
		return ErrMediaFilesClosed
	}
	return err
}

// Close waits for admitted uploads, then stops the worker before closing its
// storage owner. Store.Close also calls Close before releasing the database.
func (files *MediaFiles) Close() error {
	if files == nil {
		return nil
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.closed {
		return files.closeErr
	}
	files.closed = true
	files.closeErr = errors.Join(files.worker.close(), files.storage.Close())
	files.store.mediaMu.Lock()
	if files.store.mediaFiles == files {
		files.store.mediaFiles = nil
	}
	files.store.mediaMu.Unlock()
	return files.closeErr
}
