//go:build linux

package store

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	mediaRecoveryWorkerBatchSize = 10
	mediaRecoveryWorkerPause     = 100 * time.Millisecond
)

type mediaRecoveryWorkerPhase string
type mediaRecoveryFailure string

const (
	mediaRecoveryWorkerQueued  mediaRecoveryWorkerPhase = "queued"
	mediaRecoveryWorkerRunning mediaRecoveryWorkerPhase = "running"
	mediaRecoveryWorkerIdle    mediaRecoveryWorkerPhase = "idle"
	mediaRecoveryWorkerBlocked mediaRecoveryWorkerPhase = "blocked"
	mediaRecoveryWorkerStopped mediaRecoveryWorkerPhase = "stopped"

	mediaRecoveryFailureNone            mediaRecoveryFailure = ""
	mediaRecoveryFailureStagingUnproven mediaRecoveryFailure = "staging-unproven"
	mediaRecoveryFailureStagingLimit    mediaRecoveryFailure = "staging-limit"
	mediaRecoveryFailureObjectUnproven  mediaRecoveryFailure = "object-unproven"
	mediaRecoveryFailureStorage         mediaRecoveryFailure = "storage-unavailable"
	mediaRecoveryFailureState           mediaRecoveryFailure = "inconsistent-state"
	mediaRecoveryFailureOther           mediaRecoveryFailure = "recovery-failed"
)

var (
	errMediaRecoveryWorkerBlocked = errors.New("media recovery requires explicit retry")
	errMediaRecoveryWorkerActive  = errors.New("media recovery worker is already active")
	errMediaRecoveryWorkerClosed  = errors.New("media recovery worker is closed")
	errMediaRecoveryWorkerBusy    = errors.New("media recovery is not awaiting retry")
)

type mediaRecoveryWorkerStatus struct {
	Phase     mediaRecoveryWorkerPhase
	Completed int64
	Saturated bool
	Failure   mediaRecoveryFailure
}

// mediaRecoveryWorker owns no storage handle or database. The caller must stop
// and wait for it before closing the storage owner and its Store.
type mediaRecoveryWorker struct {
	storage *mediaStorage
	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	status  mediaRecoveryWorkerStatus
}

// startMediaRecoveryWorker schedules one initial pass; later producer attempts
// must notify it after releasing the storage lock. Errors latch until retry.
func startMediaRecoveryWorker(ctx context.Context, storage *mediaStorage) (*mediaRecoveryWorker, error) {
	if ctx == nil || storage == nil || storage.store == nil || storage.store.db == nil {
		return nil, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	storage.mu.Lock()
	if storage.closed {
		storage.mu.Unlock()
		return nil, errMediaStorageClosed
	}
	if storage.recoveryWorkerActive {
		storage.mu.Unlock()
		return nil, errMediaRecoveryWorkerActive
	}
	if err := ctx.Err(); err != nil {
		storage.mu.Unlock()
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	worker := &mediaRecoveryWorker{
		storage: storage, ctx: workerCtx, cancel: cancel,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		status: mediaRecoveryWorkerStatus{Phase: mediaRecoveryWorkerQueued},
	}
	storage.recoveryWorkerActive = true
	storage.mu.Unlock()
	worker.wake <- struct{}{}
	go worker.run()
	return worker, nil
}

func (worker *mediaRecoveryWorker) snapshot() mediaRecoveryWorkerStatus {
	if worker == nil {
		return mediaRecoveryWorkerStatus{Phase: mediaRecoveryWorkerStopped}
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.status
}

// signalLocked coalesces notifications; no per-upload work is queued.
func (worker *mediaRecoveryWorker) signalLocked() {
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

func (worker *mediaRecoveryWorker) notify() error {
	if worker == nil {
		return ErrDomainInput
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.ctx.Err() != nil || worker.status.Phase == mediaRecoveryWorkerStopped {
		return errMediaRecoveryWorkerClosed
	}
	if worker.status.Phase == mediaRecoveryWorkerBlocked {
		return errMediaRecoveryWorkerBlocked
	}
	if worker.status.Phase == mediaRecoveryWorkerIdle {
		worker.status.Phase = mediaRecoveryWorkerQueued
	}
	worker.signalLocked()
	return nil
}

// retry is an explicit foreground decision after a blocked recovery; ordinary
// producer notifications must not silently retry an uncertain cleanup.
func (worker *mediaRecoveryWorker) retry() error {
	if worker == nil {
		return ErrDomainInput
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.ctx.Err() != nil || worker.status.Phase == mediaRecoveryWorkerStopped {
		return errMediaRecoveryWorkerClosed
	}
	if worker.status.Phase != mediaRecoveryWorkerBlocked {
		return errMediaRecoveryWorkerBusy
	}
	worker.status.Phase = mediaRecoveryWorkerQueued
	worker.status.Failure = mediaRecoveryFailureNone
	worker.signalLocked()
	return nil
}

func (worker *mediaRecoveryWorker) close() error {
	if worker == nil {
		return ErrDomainInput
	}
	worker.cancel()
	<-worker.done
	return nil
}

func mediaRecoveryFailureFor(err error) mediaRecoveryFailure {
	switch {
	case errors.Is(err, errMediaStagingUnproven):
		return mediaRecoveryFailureStagingUnproven
	case errors.Is(err, errMediaStagingLimit):
		return mediaRecoveryFailureStagingLimit
	case errors.Is(err, errMediaRecoveryUnproven):
		return mediaRecoveryFailureObjectUnproven
	case errors.Is(err, ErrUnsafePath), errors.Is(err, errMediaStorageClosed):
		return mediaRecoveryFailureStorage
	case errors.Is(err, ErrDomainState):
		return mediaRecoveryFailureState
	default:
		return mediaRecoveryFailureOther
	}
}

func (worker *mediaRecoveryWorker) run() {
	defer func() {
		worker.mu.Lock()
		worker.status.Phase = mediaRecoveryWorkerStopped
		worker.mu.Unlock()
		worker.storage.mu.Lock()
		worker.storage.recoveryWorkerActive = false
		worker.storage.mu.Unlock()
		close(worker.done)
	}()
	for {
		select {
		case <-worker.ctx.Done():
			return
		case <-worker.wake:
		}
		worker.mu.Lock()
		if worker.ctx.Err() != nil {
			worker.mu.Unlock()
			return
		}
		if worker.status.Phase == mediaRecoveryWorkerBlocked {
			worker.mu.Unlock()
			continue
		}
		worker.status.Phase = mediaRecoveryWorkerRunning
		worker.status.Failure = mediaRecoveryFailureNone
		worker.mu.Unlock()
		for {
			completed, more, err := worker.storage.recoverAuditedMediaBatch(worker.ctx, mediaRecoveryWorkerBatchSize)
			worker.mu.Lock()
			if int64(completed) > maxSafeInteger-worker.status.Completed {
				worker.status.Completed = maxSafeInteger
				worker.status.Saturated = true
			} else {
				worker.status.Completed += int64(completed)
			}
			if worker.ctx.Err() != nil {
				worker.mu.Unlock()
				return
			}
			if err != nil {
				worker.status.Phase = mediaRecoveryWorkerBlocked
				worker.status.Failure = mediaRecoveryFailureFor(err)
				worker.mu.Unlock()
				break
			}
			if !more {
				// A producer may have signaled after the live page was read. Do
				// not report idle until that coalesced notification is checked.
				select {
				case <-worker.wake:
					more = true
				default:
				}
				if !more {
					worker.status.Phase = mediaRecoveryWorkerIdle
					worker.mu.Unlock()
					break
				}
			}
			worker.mu.Unlock()
			timer := time.NewTimer(mediaRecoveryWorkerPause)
			select {
			case <-worker.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
