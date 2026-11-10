package store

import (
	"errors"
	"testing"
	"time"
)

func TestDomainMutationIgnoredPublicationWritesWithholdEveryEffect(t *testing.T) {
	for _, failure := range []struct{ table, verb string }{
		{"current_projections", "INSERT"}, {"sync_changes", "INSERT"}, {"api_idempotency", "INSERT"},
		{"mutation_events", "INSERT"}, {"sessions", "UPDATE"},
	} {
		t.Run(failure.table, func(t *testing.T) {
			s, locations, auth, token := locationFixture(t)
			now := auth.now().Add(time.Minute)
			auth.now = func() time.Time { return now }
			execSQL(t, s.db, "CREATE TRIGGER ignore_domain_write BEFORE "+failure.verb+" ON "+failure.table+" BEGIN SELECT RAISE(IGNORE); END")
			before := domainState(t, s)
			input := LocationCreate{Code: "IGNORED", Name: "Never publish incomplete success"}
			if value, err := locations.Create(t.Context(), token, "ignored-publication-key", 0, input); err == nil || value != (Location{}) {
				t.Fatal("ignored mandatory write returned a resource", failure.table, value, err)
			}
			requireDomainState(t, s, before)
			execSQL(t, s.db, "DROP TRIGGER ignore_domain_write")
			if _, err := locations.Create(t.Context(), token, "ignored-publication-key", 0, input); err != nil {
				t.Fatal("failed mandatory write retained a partial key or unusable transaction", err)
			}
		})
	}
}

func TestDomainMutationIgnoredAuditCannotAcceptANewNoopReceipt(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	original := createLocation(t, locations, token, "BOX", nil)
	execSQL(t, s.db, "CREATE TRIGGER ignore_domain_audit BEFORE INSERT ON mutation_events BEGIN SELECT RAISE(IGNORE); END")
	before := domainState(t, s)
	value, err := runDomainMutation(t.Context(), auth, token, "ignored-noop-audit-key", "test_noop_audit", struct{}{}, func(m *mutationTx) (Location, error) {
		return loadLocation(m.ctx, m.tx, original.ID)
	})
	if !errors.Is(err, ErrDomainState) || value != (Location{}) {
		t.Fatal("receipt-only no-op silently omitted immutable audit", value, err)
	}
	requireDomainState(t, s, before)
}

func TestDomainReadsAndOriginalReplaysRequireOneRealSessionActivityWrite(t *testing.T) {
	s, locations, auth, token := locationFixture(t)
	input := LocationCreate{Code: "BOX", Name: "Original"}
	original, err := locations.Create(t.Context(), token, "real-session-original-key", 0, input)
	if err != nil {
		t.Fatal(err)
	}
	now := auth.now().Add(time.Minute)
	auth.now = func() time.Time { return now }
	execSQL(t, s.db, "CREATE TRIGGER ignore_domain_activity BEFORE UPDATE ON sessions BEGIN SELECT RAISE(IGNORE); END")
	before := domainState(t, s)
	if value, err := locations.Get(t.Context(), token, original.ID); !errors.Is(err, ErrDomainState) || value != (Location{}) {
		t.Fatal("ignored read activity returned an apparently completed read", value, err)
	}
	requireDomainState(t, s, before)
	if value, err := locations.Create(t.Context(), token, "real-session-original-key", 0, input); !errors.Is(err, ErrDomainState) || value != (Location{}) {
		t.Fatal("ignored replay activity exposed a frozen acknowledgement", value, err)
	}
	requireDomainState(t, s, before)
	execSQL(t, s.db, "DROP TRIGGER ignore_domain_activity")
	if value, err := locations.Create(t.Context(), token, "real-session-original-key", 0, input); err != nil || value != original {
		t.Fatal("explicit reconciliation lost its original result after activity rollback", value, err)
	}
}

func TestDomainMutationRejectsReceiptChangedDuringSessionActivity(t *testing.T) {
	for _, failure := range []struct{ name, statement string }{
		{"fingerprint", "UPDATE api_idempotency SET request_sha256='tampered' WHERE operation='post_locations'"},
		{"operation", "UPDATE api_idempotency SET operation='tampered' WHERE operation='post_locations'"},
		{"status", "UPDATE api_idempotency SET response_status=201 WHERE operation='post_locations'"},
		{"body", "UPDATE api_idempotency SET response_json='{}' WHERE operation='post_locations'"},
		{"creation", "UPDATE api_idempotency SET created_at='2001-01-01T00:00:00Z' WHERE operation='post_locations'"},
		{"expiry", "UPDATE api_idempotency SET expires_at='2099-01-01T00:00:00Z' WHERE operation='post_locations'"},
		{"audit", `INSERT OR REPLACE INTO mutation_events(id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json,note)
 SELECT id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,'tampered',result_json,note FROM mutation_events WHERE operation='post_locations'`},
	} {
		for _, replay := range []bool{false, true} {
			name := failure.name + "/new mutation"
			if replay {
				name = failure.name + "/original-key replay"
			}
			t.Run(name, func(t *testing.T) {
				s, locations, _, token := locationFixture(t)
				input := LocationCreate{Code: "BOX", Name: "Receipt must remain exact"}
				const key = "activity-receipt-original"
				var original Location
				var err error
				if replay {
					original, err = locations.Create(t.Context(), token, key, 0, input)
					if err != nil {
						t.Fatal(err)
					}
				}
				execSQL(t, s.db, "CREATE TRIGGER tamper_domain_receipt AFTER UPDATE OF last_seen_at ON sessions BEGIN "+failure.statement+"; END")
				before := domainState(t, s)
				value, err := locations.Create(t.Context(), token, key, 0, input)
				if !errors.Is(err, ErrDomainState) || value != (Location{}) {
					t.Fatal("changed receipt returned a successful mutation or replay", value, err)
				}
				requireDomainState(t, s, before)
				execSQL(t, s.db, "DROP TRIGGER tamper_domain_receipt")
				value, err = locations.Create(t.Context(), token, key, 0, input)
				if err != nil || (replay && value != original) {
					t.Fatal("rolled-back receipt change prevented explicit reconciliation", value, err)
				}
			})
		}
	}
}

func TestDomainReplayRequiresOriginalAuditScope(t *testing.T) {
	for _, fields := range []struct{ name, epoch, operation string }{
		{"epoch", "'different-epoch'", "operation"},
		{"operation", "dataset_epoch", "'different-operation'"},
	} {
		t.Run(fields.name, func(t *testing.T) {
			s, locations, _, token := locationFixture(t)
			input := LocationCreate{Code: "BOX", Name: "Original audit scope"}
			const key = "original-audit-scope"
			if _, err := locations.Create(t.Context(), token, key, 0, input); err != nil {
				t.Fatal(err)
			}
			execSQL(t, s.db, `INSERT OR REPLACE INTO mutation_events(id,admin_id,dataset_epoch,operation,idempotency_key,occurred_at,reason,result_json,note)
 SELECT id,admin_id,`+fields.epoch+`,`+fields.operation+`,idempotency_key,occurred_at,reason,result_json,note FROM mutation_events WHERE operation='post_locations'`)
			before := domainState(t, s)
			if value, err := locations.Create(t.Context(), token, key, 0, input); !errors.Is(err, ErrDomainState) || value != (Location{}) {
				t.Fatal("corrupt audit scope exposed a frozen reply", value, err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestDomainMutationRejectsReceiptChangedDuringAuditInsertion(t *testing.T) {
	s, locations, _, token := locationFixture(t)
	input := LocationCreate{Code: "BOX", Name: "Keep the original expiry"}
	execSQL(t, s.db, `CREATE TRIGGER tamper_domain_receipt AFTER INSERT ON mutation_events
 BEGIN UPDATE api_idempotency SET expires_at='2099-01-01T00:00:00Z' WHERE key=NEW.idempotency_key; END`)
	before := domainState(t, s)
	if value, err := locations.Create(t.Context(), token, "audit-receipt-original", 0, input); !errors.Is(err, ErrDomainState) || value != (Location{}) {
		t.Fatal("changed receipt returned a successful mutation", value, err)
	}
	requireDomainState(t, s, before)
	execSQL(t, s.db, "DROP TRIGGER tamper_domain_receipt")
	if _, err := locations.Create(t.Context(), token, "audit-receipt-original", 0, input); err != nil {
		t.Fatal("rolled-back receipt change prevented explicit reconciliation", err)
	}
}

func TestDomainReadsAndOriginalReplaysRejectAuthorizationChangedDuringActivity(t *testing.T) {
	for _, failure := range []struct {
		name, statement string
		want            error
	}{
		{"revoked", "UPDATE sessions SET revoked_at='2000-01-01T00:00:00Z' WHERE id=NEW.id", ErrAuthentication},
		{"epoch", "UPDATE sync_state SET dataset_epoch='changed-during-activity' WHERE singleton=1", ErrAuthentication},
		{"mandatory password change", "UPDATE admin_accounts SET must_change_password=1", ErrPasswordChangeRequired},
		{"expiry", "", ErrAuthentication},
	} {
		t.Run(failure.name, func(t *testing.T) {
			s, locations, auth, token := locationFixture(t)
			input := LocationCreate{Code: "BOX", Name: "Original"}
			original, err := locations.Create(t.Context(), token, "activity-revalidation-original", 0, input)
			if err != nil {
				t.Fatal(err)
			}
			base := auth.now()
			if failure.statement != "" {
				execSQL(t, s.db, "CREATE TRIGGER revoke_during_activity AFTER UPDATE OF last_seen_at ON sessions BEGIN "+failure.statement+"; END")
			}
			before := domainState(t, s)
			for _, read := range []bool{true, false} {
				if failure.name == "expiry" {
					calls := 0
					auth.now = func() time.Time {
						calls++
						if calls > 2 {
							return base.Add(31 * 24 * time.Hour)
						}
						return base
					}
				}
				var value Location
				if read {
					value, err = locations.Get(t.Context(), token, original.ID)
				} else {
					value, err = locations.Create(t.Context(), token, "activity-revalidation-original", 0, input)
				}
				if !errors.Is(err, failure.want) || value != (Location{}) {
					t.Fatal("activity-time authorization change exposed a read or frozen reply", read, err)
				}
				requireDomainState(t, s, before)
			}
			if failure.statement != "" {
				execSQL(t, s.db, "DROP TRIGGER revoke_during_activity")
			}
			auth.now = func() time.Time { return base }
			if value, err := locations.Create(t.Context(), token, "activity-revalidation-original", 0, input); err != nil || value != original {
				t.Fatal("rolled-back activity change prevented explicit original-key reconciliation", err)
			}
		})
	}
}

type domainCountResult struct {
	rows int64
	err  error
}

func (r domainCountResult) LastInsertId() (int64, error) { return 0, nil }
func (r domainCountResult) RowsAffected() (int64, error) { return r.rows, r.err }

func TestDomainRequiredRowPreservesErrorsAndRejectsNonexactCounts(t *testing.T) {
	for _, count := range []int64{-1, 0, 1, 2} {
		err := requireDomainRow(domainCountResult{rows: count}, nil)
		if count == 1 && err != nil || count != 1 && !errors.Is(err, ErrDomainState) {
			t.Fatal("single-row guard accepted an unknown/ignored/multiple write or rejected one real row", count, err)
		}
	}
	failure := errors.New("driver statement/count failure")
	if err := requireDomainRow(nil, failure); !errors.Is(err, failure) {
		t.Fatal("single-row guard masked the original SQL failure", err)
	}
	if err := requireDomainRow(domainCountResult{err: failure}, nil); !errors.Is(err, failure) {
		t.Fatal("single-row guard masked a driver RowsAffected failure", err)
	}
}
