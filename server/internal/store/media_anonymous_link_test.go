//go:build linux

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

type anonymousLinkFixture struct {
	store   *Store
	auth    *Auth
	token   string
	storage *mediaStorage
	stages  []*mediaStage
	pending []pendingMediaObject
}

func newAnonymousLinkFixture(t *testing.T) anonymousLinkFixture {
	t.Helper()
	s, auth, _, _, token := completedAuthFixture(t)
	library, err := NewMediaLibrary(auth)
	if err != nil {
		t.Fatal(err)
	}
	storage := testMediaStorage(t, s)
	stages := anonymousJournalStages(t, filepath.Join(storage.path, "staging"))
	storage.mu.Lock()
	pending, err := library.journalMediaObjects(t.Context(), token, stages, storage.objects)
	storage.mu.Unlock()
	if err != nil || len(pending) != 2 {
		t.Fatal("anonymous journal fixture failed", pending, err)
	}
	return anonymousLinkFixture{store: s, auth: auth, token: token, storage: storage, stages: stages, pending: pending}
}

func (f anonymousLinkFixture) link(t *testing.T, key string, pending []pendingMediaObject) (ActionAck, error) {
	t.Helper()
	f.storage.mu.Lock()
	defer f.storage.mu.Unlock()
	return runDomainMutation(t.Context(), f.auth, f.token, key, "test_link_pending", struct{}{}, func(m *mutationTx) (ActionAck, error) {
		if err := m.materializeAnonymousMediaStages(f.stages, pending, f.storage.objects); err != nil {
			return ActionAck{}, err
		}
		return ActionAck{OK: true, OperationID: m.eventID, Revision: 1}, nil
	})
}

func TestJournaledAnonymousMediaLinksExactInodes(t *testing.T) {
	f := newAnonymousLinkFixture(t)
	result, err := f.link(t, "anonymous-link-success", f.pending)
	if errors.Is(err, errMediaAnonymousUnsupported) {
		t.Skipf("anonymous inode linking is unavailable on this filesystem: %v", err)
	}
	if err != nil || !result.OK {
		t.Fatal("journaled anonymous stages were not linked", result, err)
	}
	for index, row := range f.pending {
		path := filepath.Join(f.storage.path, "staging", row.StageName)
		var actual unix.Stat_t
		if err = unix.Lstat(path, &actual); err != nil || int64(actual.Dev) != row.StageDevice || int64(actual.Ino) != row.StageInode || actual.Nlink != 1 || actual.Mode&0o7777 != 0o600 {
			t.Fatal("linked stage is not the journaled inode", row, actual, err)
		}
		if f.stages[index].anonymous || f.stages[index].file != nil {
			t.Fatal("linked stage retained an anonymous descriptor")
		}
		if err = f.stages[index].preserve(); err != nil {
			t.Fatal(err)
		}
	}
	completed, more, err := f.storage.recoverPendingMediaBatch(t.Context(), 10)
	if err != nil || completed != 2 || more {
		t.Fatal("linked unreferenced stages were not recoverable", completed, more, err)
	}
}

func TestJournaledAnonymousMediaRejectsChangedProofAndBytes(t *testing.T) {
	for _, change := range []string{"proof", "bytes", "epoch"} {
		t.Run(change, func(t *testing.T) {
			f := newAnonymousLinkFixture(t)
			pending := append([]pendingMediaObject(nil), f.pending...)
			switch change {
			case "proof":
				pending[0].StageInode++
			case "bytes":
				if _, err := f.stages[0].file.WriteAt([]byte("X"), 0); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				pending[0].Epoch = "different-epoch"
			}
			before := domainState(t, f.store)
			result, err := f.link(t, "anonymous-link-"+change, pending)
			if err == nil || result != (ActionAck{}) {
				t.Fatal("changed original proof or bytes were accepted", result, err)
			}
			requireDomainState(t, f.store, before)
			for _, row := range f.pending {
				if _, err = os.Lstat(filepath.Join(f.storage.path, "staging", row.StageName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed proof check published a stage name", err)
				}
			}
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
				t.Fatal("failed proof check lost committed journal rows", count)
			}
		})
	}
}

func TestJournaledAnonymousMediaPreservesFirstLinkWhenSecondConflicts(t *testing.T) {
	f := newAnonymousLinkFixture(t)
	collision := filepath.Join(f.storage.path, "staging", f.pending[1].StageName)
	if err := os.WriteFile(collision, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := domainState(t, f.store)
	result, err := f.link(t, "anonymous-link-collision", f.pending)
	if errors.Is(err, errMediaAnonymousUnsupported) {
		if closeErr := f.stages[1].discard(); !errors.Is(closeErr, errMediaStageChanged) {
			t.Fatal("failed to release colliding anonymous stage", closeErr)
		}
		t.Skipf("anonymous inode linking is unavailable on this filesystem: %v", err)
	}
	if !errors.Is(err, errMediaStageChanged) || result != (ActionAck{}) {
		t.Fatal("second occupied name was accepted", result, err)
	}
	requireDomainState(t, f.store, before)
	first := filepath.Join(f.storage.path, "staging", f.pending[0].StageName)
	var actual unix.Stat_t
	if err = unix.Lstat(first, &actual); err != nil || int64(actual.Ino) != f.pending[0].StageInode {
		t.Fatal("the first linked inode lost its journal proof", actual, err)
	}
	if err = f.stages[0].preserve(); err != nil {
		t.Fatal(err)
	}
	if err = f.stages[1].discard(); !errors.Is(err, errMediaStageChanged) {
		t.Fatal("colliding anonymous stage did not report the conflict", err)
	}
	if value, err := os.ReadFile(collision); err != nil || string(value) != "unrelated" {
		t.Fatal("linking changed an unrelated file", string(value), err)
	}
	if err = os.Remove(collision); err != nil {
		t.Fatal(err)
	}
	completed, more, err := f.storage.recoverPendingMediaBatch(t.Context(), 10)
	if err != nil || completed != 2 || more {
		t.Fatal("partial link could not be reconciled", completed, more, err)
	}
}

func TestJournaledAnonymousMediaRetainsProofWhenLateSQLFails(t *testing.T) {
	for _, test := range []struct{ name, setup string }{
		{"late deletion", `CREATE TRIGGER anonymous_link_fault AFTER INSERT ON mutation_events WHEN NEW.operation='test_link_pending' BEGIN DELETE FROM media_pending_objects; END`},
		{"deferred commit", `CREATE TABLE anonymous_link_fk(value TEXT REFERENCES media(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER anonymous_link_fault AFTER INSERT ON mutation_events WHEN NEW.operation='test_link_pending' BEGIN INSERT INTO anonymous_link_fk VALUES('missing'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAnonymousLinkFixture(t)
			execSQL(t, f.store.db, test.setup)
			before := domainState(t, f.store)
			result, err := f.link(t, "anonymous-link-late-failure", f.pending)
			if errors.Is(err, errMediaAnonymousUnsupported) {
				t.Skipf("anonymous inode linking is unavailable on this filesystem: %v", err)
			}
			if err == nil || result != (ActionAck{}) {
				t.Fatal("late failure did not roll back the mutation", result, err)
			}
			requireDomainState(t, f.store, before)
			if count := scalar[int](t, f.store.db, "SELECT COUNT(*) FROM media_pending_objects"); count != 2 {
				t.Fatal("late failure lost the prior committed journal", count)
			}
			for index, row := range f.pending {
				var actual unix.Stat_t
				if err = unix.Lstat(filepath.Join(f.storage.path, "staging", row.StageName), &actual); err != nil || int64(actual.Ino) != row.StageInode {
					t.Fatal("late failure lost a journaled stage", actual, err)
				}
				if err = f.stages[index].preserve(); err != nil {
					t.Fatal(err)
				}
			}
			completed, more, err := f.storage.recoverPendingMediaBatch(t.Context(), 10)
			if err != nil || completed != 2 || more {
				t.Fatal("late failure was not recoverable", completed, more, err)
			}
		})
	}
}
