package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCatalogOwnedAdmissionBoundsNeverClaimKeysOrRepairValues(t *testing.T) {
	s, catalog, _, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	before := domainState(t, s)
	for _, writer := range catalogLinkMutators(catalog) {
		for _, id := range []string{"", strings.Repeat("界", 129), string([]byte{0xff})} {
			for _, check := range []func() (any, error){
				func() (any, error) {
					return writer.create(t.Context(), catalog, token, "invalid-owned-id", id, 1, "Value")
				},
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "invalid-owned-id", id, 1, "Value")
				},
				func() (any, error) { return writer.delete(t.Context(), token, "invalid-owned-id", id, 1) },
			} {
				if value, err := check(); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("invalid ID was repaired or exposed a resource", writer.kind, err)
				}
			}
		}
		for _, revision := range []int64{0, -1, maxSafeInteger + 1} {
			want := ErrDomainInput
			if revision == 0 {
				want = ErrPreconditionRequired
			}
			for _, check := range []func() (any, error){
				func() (any, error) {
					return writer.create(t.Context(), catalog, token, "invalid-owned-proof", item.ID, revision, "Value")
				},
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "invalid-owned-proof", "row", revision, "Value")
				},
				func() (any, error) { return writer.delete(t.Context(), token, "invalid-owned-proof", "row", revision) },
			} {
				if value, err := check(); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("owned mutation skipped its original positive safe proof", writer.kind, err)
				}
			}
		}
		for _, invalid := range []string{string([]byte{0xff}), strings.Repeat("x", 10001)} {
			if value, err := writer.create(t.Context(), catalog, token, "invalid-owned-text", item.ID, 1, invalid); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("invalid text was accepted by creation", writer.kind, err)
			}
			if value, err := writer.patch(t.Context(), catalog, token, "invalid-owned-text", "row", 1, invalid); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("invalid text was accepted by editing", writer.kind, err)
			}
		}
		if value, err := writer.create(t.Context(), catalog, token, "short", item.ID, 1, "Value"); !errors.Is(err, ErrDomainInput) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("an invalid key was claimed", writer.kind, err)
		}
	}
	for _, input := range []BarcodeWrite{
		{"EAN_13", "4006381333932"}, {"EAN_8", "96385075"}, {"UPC_A", "036000291453"}, {"UPC_E", "04252615"},
		{"UPC_E", "24252614"}, {"EAN_13", " 4006381333931"}, {"EAN_13", "４006381333931"},
		{"CODE_128", "條碼"}, {"OTHER", ""}, {"QR_CODE", strings.Repeat("😀", 513)}, {"unknown", "00123"},
	} {
		if value, err := catalog.CreateBarcode(t.Context(), token, "invalid-real-checksum", item.ID, 1, input); !errors.Is(err, ErrDomainInput) || value.ID != "" {
			t.Fatal("new source bypassed actual checksum/namespace/ASCII validation", err)
		}
		if value, err := catalog.PatchBarcode(t.Context(), token, "invalid-real-checksum", "row", 1, input); !errors.Is(err, ErrDomainInput) || value.ID != "" {
			t.Fatal("replacement bypassed actual checksum/namespace/ASCII validation", err)
		}
	}
	for _, alias := range []string{"", strings.Repeat("😀", 301)} {
		if _, err := catalog.CreateAlias(t.Context(), token, "invalid-alias-bounds", item.ID, 1, alias); !errors.Is(err, ErrDomainInput) {
			t.Fatal("alias exceeded its exact code-point bound", err)
		}
	}
	for _, input := range []AttributeWrite{{"", "Value"}, {strings.Repeat("😀", 101), "Value"}, {string([]byte{0xff}), "Value"}} {
		if _, err := catalog.CreateAttribute(t.Context(), token, "invalid-attribute-key", item.ID, 1, input); !errors.Is(err, ErrDomainInput) {
			t.Fatal("invalid attribute key was admitted", err)
		}
	}
	for _, input := range []AttributePatch{{}, {AttributeKey: testPointer("")}, {AttributeKey: testPointer(strings.Repeat("😀", 101))}, {AttributeValue: testPointer(string([]byte{0xff}))}} {
		if _, err := catalog.PatchAttribute(t.Context(), token, "invalid-attribute-patch", "row", 1, input); !errors.Is(err, ErrDomainInput) {
			t.Fatal("empty/invalid partial attribute input was admitted", err)
		}
	}
	if _, err := catalog.deleteCatalogLink(t.Context(), token, "invalid-owned-kind", "unknown", "row", 1); !errors.Is(err, ErrDomainInput) {
		t.Fatal("unrecognized private table selection was admitted", err)
	}
	requireDomainState(t, s, before)
	for index, input := range []BarcodeWrite{{"EAN_13", "4006381333931"}, {"EAN_8", "96385074"}, {"UPC_A", "036000291452"}, {"UPC_E", "04252614"}, {"CODE_128", "\x00GS1\x1d00123"}, {"QR_CODE", strings.Repeat("😀", 512)}, {"OTHER", " https://example.invalid/opaque?leading=0001 "}} {
		owner := getCatalogItem(t, catalog, token, item.ID)
		value, err := catalog.CreateBarcode(t.Context(), token, fmt.Sprintf("valid-normalized-source-%d", index), item.ID, owner.Revision, input)
		if err != nil || value.RawValue != input.RawValue || value.NormalizedValue != input.RawValue {
			t.Fatal("valid new barcode lost its exact representation", err)
		}
	}
	owner := getCatalogItem(t, catalog, token, item.ID)
	if _, err := catalog.CreateAlias(t.Context(), token, "maximum-owned-alias", item.ID, owner.Revision, strings.Repeat("😀", 300)); err != nil {
		t.Fatal("valid alias maximum was narrowed", err)
	}
	owner = getCatalogItem(t, catalog, token, item.ID)
	if _, err := catalog.CreateAttribute(t.Context(), token, "maximum-owned-attribute", item.ID, owner.Revision, AttributeWrite{strings.Repeat("😀", 100), strings.Repeat("😀", 10000)}); err != nil {
		t.Fatal("valid key/value Unicode maxima were narrowed", err)
	}
}

func TestCatalogOwnedMissingAndStaleSubjectsWithholdAllWrites(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			writer := catalogLinkMutators(catalog)[index]
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			value, err := writer.create(t.Context(), catalog, token, "owned-stale-baseline", item.ID, 1, "Before")
			row := catalogLinkResult(t, value, err)
			before := domainState(t, s)
			for _, missing := range []bool{false, true} {
				id, proof, want := row.ID, int64(2), ErrRevisionConflict
				owner := item.ID
				if missing {
					id, owner, proof, want = "missing-row", "missing-owner", 1, ErrNotFound
				}
				for _, check := range []func() (any, error){
					func() (any, error) {
						return writer.create(t.Context(), catalog, token, "owned-stale-create", owner, 1, "New")
					},
					func() (any, error) {
						return writer.patch(t.Context(), catalog, token, "owned-stale-patch", id, proof, "Before")
					},
					func() (any, error) { return writer.delete(t.Context(), token, "owned-stale-delete", id, proof) },
				} {
					if value, err := check(); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
						t.Fatal("missing/stale subject was rebased, treated as no-op or acknowledged", err)
					}
				}
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogOwnedFingerprintsBindOperationInputSubjectAndOriginalProof(t *testing.T) {
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			writer := catalogLinkMutators(catalog)[index]
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			value, err := writer.create(t.Context(), catalog, token, "owned-fingerprint-create", item.ID, 1, "Before")
			row := catalogLinkResult(t, value, err)
			value, err = writer.create(t.Context(), catalog, token, "owned-fingerprint-second", item.ID, 2, "Second")
			other := catalogLinkResult(t, value, err)
			if _, err := writer.patch(t.Context(), catalog, token, "owned-fingerprint-patch", row.ID, 1, "First"); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.patch(t.Context(), catalog, token, "owned-fingerprint-newer", row.ID, 2, "Current"); err != nil {
				t.Fatal(err)
			}
			before := domainState(t, s)
			for _, check := range []func() (any, error){
				func() (any, error) {
					return writer.create(t.Context(), catalog, token, "owned-fingerprint-create", item.ID, 1, "Changed")
				},
				func() (any, error) {
					return writer.create(t.Context(), catalog, token, "owned-fingerprint-create", item.ID, 2, "Before")
				},
				func() (any, error) {
					return writer.create(t.Context(), catalog, token, "owned-fingerprint-create", "different-owner", 1, "Before")
				},
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "owned-fingerprint-patch", row.ID, 1, "Changed")
				},
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "owned-fingerprint-patch", row.ID, 2, "First")
				},
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "owned-fingerprint-patch", other.ID, 1, "First")
				},
				func() (any, error) { return writer.delete(t.Context(), token, "owned-fingerprint-patch", row.ID, 1) },
			} {
				if value, err := check(); !errors.Is(err, ErrIdempotencyConflict) || !reflect.ValueOf(value).IsZero() {
					t.Fatal("changed owned command reused a principal/epoch key", err)
				}
			}
			for _, otherWriter := range catalogLinkMutators(catalog) {
				if otherWriter.kind != kind {
					if _, err := otherWriter.create(t.Context(), catalog, token, "owned-fingerprint-create", item.ID, 1, "Before"); !errors.Is(err, ErrIdempotencyConflict) {
						t.Fatal("one operation's key became another owned producer", err)
					}
				}
			}
			requireDomainState(t, s, before)
			ack, err := writer.delete(t.Context(), token, "owned-fingerprint-delete", row.ID, 3)
			if err != nil || ack.Revision != 4 {
				t.Fatal(err)
			}
			before = domainState(t, s)
			if _, err := writer.delete(t.Context(), token, "owned-fingerprint-delete", row.ID, 2); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatal("deletion retry silently replaced its original proof", err)
			}
			if _, err := writer.delete(t.Context(), token, "owned-fingerprint-delete", other.ID, 3); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatal("deletion retry silently replaced its original row", err)
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogOwnedWritesAndFrozenRepliesRequireActiveCompletedEpochSession(t *testing.T) {
	for _, state := range []string{"mandatory", "revoked", "expired", "epoch", "invalid"} {
		t.Run(state, func(t *testing.T) {
			s, catalog, auth, token := catalogFixture(t)
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			rows := make(map[string]catalogLinkReference)
			for index, writer := range catalogLinkMutators(catalog) {
				value, err := writer.create(t.Context(), catalog, token, "auth-owned-original-"+writer.kind, item.ID, int64(index+1), "Before")
				rows[writer.kind] = catalogLinkResult(t, value, err)
			}
			want := ErrAuthentication
			switch state {
			case "mandatory":
				execSQL(t, s.db, "UPDATE admin_accounts SET must_change_password=1")
				want = ErrPasswordChangeRequired
			case "revoked":
				execSQL(t, s.db, "UPDATE sessions SET revoked_at=?", stamp(auth.now()))
			case "expired":
				execSQL(t, s.db, "UPDATE sessions SET expires_at=?", stamp(auth.now()))
			case "epoch":
				execSQL(t, s.db, "UPDATE sync_state SET dataset_epoch='restored-owned-epoch'")
			case "invalid":
				token = "invalid"
			}
			before := domainState(t, s)
			for index, writer := range catalogLinkMutators(catalog) {
				row := rows[writer.kind]
				for _, check := range []func() (any, error){
					func() (any, error) {
						return writer.create(t.Context(), catalog, token, "auth-owned-new", item.ID, 4, "After")
					},
					func() (any, error) {
						return writer.patch(t.Context(), catalog, token, "auth-owned-patch", row.ID, 1, "After")
					},
					func() (any, error) { return writer.delete(t.Context(), token, "auth-owned-delete", row.ID, 1) },
					func() (any, error) {
						return writer.create(t.Context(), catalog, token, "auth-owned-original-"+writer.kind, item.ID, int64(index+1), "Before")
					},
				} {
					if value, err := check(); !errors.Is(err, want) || !reflect.ValueOf(value).IsZero() {
						t.Fatal("owned mutation/reply bypassed transactional authorization", writer.kind, err)
					}
				}
			}
			requireDomainState(t, s, before)
		})
	}
}

func TestCatalogOwnedExpiredClaimsAndCancelledCallsCannotReplayWrites(t *testing.T) {
	s, catalog, auth, token := catalogFixture(t)
	item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
	rows := make(map[string]catalogLinkReference)
	for index, writer := range catalogLinkMutators(catalog) {
		value, err := writer.create(t.Context(), catalog, token, "expiry-owned-original-"+writer.kind, item.ID, int64(index+1), "Before")
		rows[writer.kind] = catalogLinkResult(t, value, err)
	}
	later := auth.now().Add(31 * 24 * time.Hour)
	auth.now = func() time.Time { return later }
	_, fresh := login(t, auth)
	before := domainState(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for index, writer := range catalogLinkMutators(catalog) {
		if value, err := writer.create(t.Context(), catalog, fresh, "expiry-owned-original-"+writer.kind, item.ID, int64(index+1), "Before"); !errors.Is(err, ErrIdempotencyExpired) || !reflect.ValueOf(value).IsZero() {
			t.Fatal("expired owned claim was deleted/reused", err)
		}
		for _, check := range []func() (any, error){
			func() (any, error) {
				return writer.create(ctx, catalog, fresh, "cancel-owned-create", item.ID, 4, "After")
			},
			func() (any, error) {
				return writer.patch(ctx, catalog, fresh, "cancel-owned-patch", rows[writer.kind].ID, 1, "After")
			},
			func() (any, error) { return writer.delete(ctx, fresh, "cancel-owned-delete", rows[writer.kind].ID, 1) },
		} {
			if value, err := check(); !errors.Is(err, context.Canceled) || !reflect.ValueOf(value).IsZero() {
				t.Fatal("cancelled call exposed a row or acquired a new write", err)
			}
		}
	}
	requireDomainState(t, s, before)
}

func TestCatalogOwnedConcurrentKeysParentProofsAndIndependentRowReviews(t *testing.T) {
	type outcome struct {
		value any
		err   error
	}
	concurrent := func(calls []func() (any, error)) []outcome {
		start := make(chan struct{})
		results := make(chan outcome, len(calls))
		var group sync.WaitGroup
		for _, call := range calls {
			group.Go(func() {
				<-start
				value, err := call()
				results <- outcome{value, err}
			})
		}
		close(start)
		group.Wait()
		close(results)
		var values []outcome
		for result := range results {
			values = append(values, result)
		}
		return values
	}
	for index, kind := range []string{"barcode", "alias", "attribute"} {
		t.Run(kind, func(t *testing.T) {
			s, catalog, _, token := catalogFixture(t)
			writer := catalogLinkMutators(catalog)[index]
			item := createCatalogItem(t, catalog, token, ItemCreate{Name: "Owner"})
			var calls []func() (any, error)
			for range 12 {
				calls = append(calls, func() (any, error) {
					return writer.create(t.Context(), catalog, token, "concurrent-owned-original", item.ID, 1, "Original")
				})
			}
			var original any
			for _, result := range concurrent(calls) {
				if result.err != nil || original != nil && !reflect.DeepEqual(original, result.value) {
					t.Fatal("identical concurrent original keys produced different rows/replies", result.err)
				}
				original = result.value
			}
			row := catalogLinkResult(t, original, nil)
			if scalar[int](t, s.db, "SELECT COUNT(*) FROM "+writer.table) != 1 {
				t.Fatal("concurrent retries created more than one independent row")
			}
			calls = nil
			for n := range 2 {
				calls = append(calls, func() (any, error) {
					return writer.create(t.Context(), catalog, token, fmt.Sprintf("concurrent-owned-parent-%d", n), item.ID, 2, fmt.Sprintf("Second%d", n))
				})
			}
			var second catalogLinkReference
			for _, result := range concurrent(calls) {
				if result.err == nil {
					if second.ID != "" {
						t.Fatal("two different creation commands silently reused the same Item proof")
					}
					second = catalogLinkResult(t, result.value, nil)
				} else if !errors.Is(result.err, ErrRevisionConflict) {
					t.Fatal(result.err)
				}
			}
			if second.ID == "" || getCatalogItem(t, catalog, token, item.ID).Revision != 3 {
				t.Fatal("concurrent creation had no single original-proof winner")
			}
			calls = nil
			for n := range 2 {
				calls = append(calls, func() (any, error) {
					return writer.patch(t.Context(), catalog, token, fmt.Sprintf("concurrent-owned-row-%d", n), row.ID, 1, fmt.Sprintf("Winner%d", n))
				})
			}
			successes := 0
			for _, result := range concurrent(calls) {
				if result.err == nil {
					successes++
				} else if !errors.Is(result.err, ErrRevisionConflict) {
					t.Fatal(result.err)
				}
			}
			if successes != 1 {
				t.Fatal("two conflicting row reviews were rebased into overwrites")
			}
			calls = []func() (any, error){
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "independent-owned-row-one", row.ID, 2, "IndependentOne")
				},
				func() (any, error) {
					return writer.patch(t.Context(), catalog, token, "independent-owned-row-two", second.ID, 1, "IndependentTwo")
				},
			}
			for _, result := range concurrent(calls) {
				if result.err != nil {
					t.Fatal("independent row proofs were incorrectly replaced by an Item proof", result.err)
				}
			}
			if getCatalogItem(t, catalog, token, item.ID).Revision != 6 {
				t.Fatal("independent row changes lost an owner aggregate update")
			}
		})
	}
}
