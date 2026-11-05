package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCatalogOwnedEveryDependentWriteFailureRollsBackAndAllowsOriginalRetry(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		for _, operation := range []string{"create", "patch", "delete"} {
			sourceEvent := "INSERT"
			if operation == "patch" {
				sourceEvent = "UPDATE"
			} else if operation == "delete" {
				sourceEvent = "DELETE"
			}
			failures := []struct{ table, event string }{
				{"source", sourceEvent}, {"projection_revisions", "INSERT"}, {"projection_revisions", "UPDATE"},
				{"current_projections", "INSERT"}, {"current_projections", "UPDATE"}, {"search_documents", "INSERT"}, {"search_documents", "UPDATE"},
				{"unit_search_documents", "INSERT"}, {"sync_changes", "INSERT"}, {"data_revision", "UPDATE"},
				{"api_idempotency", "INSERT"}, {"mutation_events", "INSERT"}, {"sessions", "UPDATE"}, {"items", "UPDATE"},
			}
			if operation == "delete" {
				failures = append(failures, struct{ table, event string }{"current_projections", "DELETE"})
			}
			for _, failure := range failures {
				t.Run(kind+"/"+operation+"/"+failure.table+failure.event, func(t *testing.T) {
					s, catalog, auth, token := catalogFixture(t)
					writer := catalogLinkMutators(catalog)[index]
					item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
					var row catalogLinkReference
					if operation != "create" {
						value, err := writer.create(t.Context(), catalog, token, "owned-rollback-baseline", item.ID, 1, "BeforeTerm")
						row = catalogLinkResult(t, value, err)
					}
					// An unprojected dependent unit exercises counter/current/FTS inserts
					// even while the existing Item and row exercise update failures.
					execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
					now := auth.now().Add(time.Minute)
					auth.now = func() time.Time { return now }
					write := func() (any, error) {
						switch operation {
						case "create":
							return writer.create(t.Context(), catalog, token, "owned-rollback-original", item.ID, 1, "AfterTerm")
						case "patch":
							return writer.patch(t.Context(), catalog, token, "owned-rollback-original", row.ID, 1, "AfterTerm")
						default:
							return writer.delete(t.Context(), token, "owned-rollback-original", row.ID, 1)
						}
					}
					table, event := failure.table, failure.event
					if table == "source" {
						table = writer.table
					}
					before := domainState(t, s)
					beforeFTS := scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeTerm'")
					execSQL(t, s.db, "CREATE TRIGGER fail_owned_write BEFORE "+event+" ON "+table+" BEGIN SELECT RAISE(ABORT,'owned write failure'); END")
					if value, err := write(); err == nil || !reflect.ValueOf(value).IsZero() {
						t.Fatal("failed owned write exposed a successful resource/acknowledgement", err)
					}
					requireDomainState(t, s, before)
					if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'BeforeTerm'") != beforeFTS ||
						scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterTerm'") != 0 || scalar[int](t, s.db, "SELECT COUNT(*) FROM unit_search_fts") != 0 {
						t.Fatal("rolled-back source/counter/reply left partial search publication")
					}
					execSQL(t, s.db, "DROP TRIGGER fail_owned_write")
					if _, err := write(); err != nil {
						t.Fatal("rolled-back original key/proof or connection could not be explicitly reused", err)
					}
				})
			}
		}
	}
}

func TestCatalogOwnedLateAuthorizationAndDeferredCommitWithholdAllNineResults(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		for _, operation := range []string{"create", "patch", "delete"} {
			for _, failure := range []string{"late revocation", "deferred commit"} {
				t.Run(kind+"/"+operation+"/"+failure, func(t *testing.T) {
					s, catalog, _, token := catalogFixture(t)
					writer := catalogLinkMutators(catalog)[index]
					item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
					var row catalogLinkReference
					if operation != "create" {
						value, err := writer.create(t.Context(), catalog, token, "owned-late-baseline", item.ID, 1, "BeforeTerm")
						row = catalogLinkResult(t, value, err)
					}
					if failure == "late revocation" {
						execSQL(t, s.db, "CREATE TRIGGER fail_owned_commit AFTER INSERT ON mutation_events BEGIN UPDATE sessions SET revoked_at='2026-01-01T00:00:00Z'; END")
					} else {
						execSQL(t, s.db, "CREATE TABLE owned_result_guard(id TEXT REFERENCES admin_accounts(id) DEFERRABLE INITIALLY DEFERRED) STRICT")
						execSQL(t, s.db, "CREATE TRIGGER fail_owned_commit AFTER INSERT ON mutation_events BEGIN INSERT INTO owned_result_guard VALUES('missing'); END")
					}
					before := domainState(t, s)
					var value any
					var err error
					switch operation {
					case "create":
						value, err = writer.create(t.Context(), catalog, token, "owned-late-original", item.ID, 1, "AfterTerm")
					case "patch":
						value, err = writer.patch(t.Context(), catalog, token, "owned-late-original", row.ID, 1, "AfterTerm")
					case "delete":
						value, err = writer.delete(t.Context(), token, "owned-late-original", row.ID, 1)
					}
					if err == nil || !reflect.ValueOf(value).IsZero() || failure == "late revocation" && !errors.Is(err, ErrAuthentication) {
						t.Fatal("late transaction failure returned a committed-looking result", err)
					}
					requireDomainState(t, s, before)
					if scalar[int](t, s.db, "SELECT COUNT(*) FROM search_fts WHERE search_fts MATCH 'AfterTerm'") != 0 ||
						failure == "deferred commit" && scalar[int](t, s.db, "SELECT COUNT(*) FROM owned_result_guard") != 0 {
						t.Fatal("failed COMMIT left a partial search or deferred guard row")
					}
				})
			}
		}
	}
}

func TestCatalogOwnedCounterOverflowAndMissingOwnerAuthorityRollbackEarlierChanges(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		for _, scope := range []string{"row", "owner", "item search", "unit search", "catalog", "sequence", "missing owner counter", "missing row counter"} {
			for _, deletion := range []bool{false, true} {
				t.Run(kind+"/"+scope+"/"+map[bool]string{false: "patch", true: "delete"}[deletion], func(t *testing.T) {
					s, catalog, _, token := catalogFixture(t)
					writer := catalogLinkMutators(catalog)[index]
					item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
					value, err := writer.create(t.Context(), catalog, token, "owned-overflow-baseline", item.ID, 1, "BeforeTerm")
					row := catalogLinkResult(t, value, err)
					proof, want := int64(1), ErrRevisionOverflow
					switch scope {
					case "row":
						setCatalogProjectionRevision(t, s, kind, row.ID, maxSafeInteger)
						proof = maxSafeInteger
					case "owner":
						setCatalogProjectionRevision(t, s, "item", item.ID, maxSafeInteger)
					case "item search":
						setCatalogProjectionRevision(t, s, "search_doc", projectionPair("item", item.ID), maxSafeInteger)
					case "unit search":
						execSQL(t, s.db, "INSERT INTO item_units(id,item_id,asset_code,created_at,updated_at) VALUES('unit',?,'UNIT-001','2026','2026')", item.ID)
						execSQL(t, s.db, "INSERT INTO projection_revisions VALUES('search_doc',?,?)", projectionPair("unit", "unit"), maxSafeInteger)
					case "catalog":
						execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
					case "sequence":
						execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
					case "missing owner counter", "missing row counter":
						targetKind, targetID := "item", item.ID
						if scope == "missing row counter" {
							targetKind, targetID = kind, row.ID
						}
						execSQL(t, s.db, "DELETE FROM current_projections WHERE entity_type=? AND entity_id=?", targetKind, targetID)
						execSQL(t, s.db, "DELETE FROM projection_revisions WHERE entity_type=? AND entity_id=?", targetKind, targetID)
						want = ErrDomainState
					}
					before := domainState(t, s)
					if deletion {
						value, err = writer.delete(t.Context(), token, "owned-overflow-original", row.ID, proof)
					} else {
						value, err = writer.patch(t.Context(), catalog, token, "owned-overflow-original", row.ID, proof, "AfterTerm")
					}
					if !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
						t.Fatal("unsafe/missing authority exposed a successful resource or acknowledgement", err)
					}
					requireDomainState(t, s, before)
				})
			}
		}
	}
}

func TestCatalogOwnedCreationNeverReusesRetainedUUIDAndRollsBackExhaustedOwners(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		for _, failure := range []string{"retained identity", "exhausted owner"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				s, catalog, _, token := catalogFixture(t)
				writer := catalogLinkMutators(catalog)[index]
				item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
				proof, want := int64(1), ErrDomainState
				if failure == "retained identity" {
					execSQL(t, s.db, "CREATE TRIGGER reserve_owned_uuid BEFORE INSERT ON "+writer.table+" BEGIN INSERT INTO projection_revisions VALUES('"+kind+"',new.id,7); END")
				} else {
					setCatalogProjectionRevision(t, s, "item", item.ID, maxSafeInteger)
					proof, want = maxSafeInteger, ErrRevisionOverflow
				}
				before := domainState(t, s)
				if value, err := writer.create(t.Context(), catalog, token, "never-reuse-owned-identity", item.ID, proof, "AfterTerm"); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("new row reused a retained identity or partially acquired an exhausted owner", err)
				}
				requireDomainState(t, s, before)
			})
		}
	}
}

func TestCatalogOwnedNoopAtSafeLimitsCreatesOnlyImmutableReceipt(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			writer := catalogLinkMutators(catalog)[index]
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			value, err := writer.create(t.Context(), catalog, token, "owned-limit-baseline", item.ID, 1, "BeforeTerm")
			row := catalogLinkResult(t, value, err)
			setCatalogProjectionRevision(t, s, kind, row.ID, maxSafeInteger)
			setCatalogProjectionRevision(t, s, "item", item.ID, maxSafeInteger)
			execSQL(t, s.db, "UPDATE data_revision SET revision=? WHERE scope='catalog'", maxSafeInteger)
			execSQL(t, s.db, "UPDATE sqlite_sequence SET seq=? WHERE name='sync_changes'", maxSafeInteger)
			before := domainState(t, s)
			value, err = writer.patch(t.Context(), catalog, token, "owned-limit-noop", row.ID, maxSafeInteger, "BeforeTerm")
			if result := catalogLinkResult(t, value, err); result.Revision != maxSafeInteger {
				t.Fatal("no-op invented another safe-limit row revision")
			}
			after := domainState(t, s)
			for table, contents := range before {
				if table != "api_idempotency" && table != "mutation_events" && after[table] != contents {
					t.Fatal("no-op mutated authority/publication at a counter ceiling", table)
				}
			}
		})
	}
}
