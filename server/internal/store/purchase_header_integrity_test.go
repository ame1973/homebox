package store

import (
	"reflect"
	"testing"
	"time"
)

func TestPurchaseHeadersNeverFreezeIgnoredRewrittenOrMissingSourceAndCorrectionContext(t *testing.T) {
	for _, kind := range []string{"create", "patch", "noop"} {
		for _, failure := range []string{"ignored source", "rewritten source", "ignored context", "rewritten context", "deleted context", "context rewrites source", "reseeded header"} {
			if kind == "create" && (failure == "ignored context" || failure == "rewritten context" || failure == "deleted context" || failure == "context rewrites source") ||
				kind == "noop" && (failure == "ignored source" || failure == "rewritten source" || failure == "reseeded header") {
				continue
			}
			t.Run(kind+"/"+failure, func(t *testing.T) {
				fixture, purchases := purchaseCommands(t)
				s := fixture.store
				now := fixture.auth.now().Add(time.Minute)
				fixture.auth.now = func() time.Time { return now }
				sourceVerb := "UPDATE"
				if kind == "create" {
					sourceVerb = "INSERT"
				}
				switch failure {
				case "ignored source":
					execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity BEFORE "+sourceVerb+" ON purchases BEGIN SELECT RAISE(IGNORE); END")
				case "rewritten source":
					execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity AFTER "+sourceVerb+" ON purchases WHEN NEW.merchant_name<>'Rewritten actual header' BEGIN UPDATE purchases SET merchant_name='Rewritten actual header' WHERE id=NEW.id; END")
				case "ignored context":
					execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity BEFORE INSERT ON purchase_header_corrections BEGIN SELECT RAISE(IGNORE); END")
				case "rewritten context":
					execSQL(t, s.db, "DROP TRIGGER purchase_header_corrections_no_update")
					execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity AFTER INSERT ON purchase_header_corrections BEGIN UPDATE purchase_header_corrections SET context_json='{}' WHERE mutation_event_id=NEW.mutation_event_id; END")
				case "deleted context":
					execSQL(t, s.db, "DROP TRIGGER purchase_header_corrections_no_delete")
					execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity AFTER INSERT ON purchase_header_corrections BEGIN DELETE FROM purchase_header_corrections WHERE mutation_event_id=NEW.mutation_event_id; END")
				case "context rewrites source":
					execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity AFTER INSERT ON purchase_header_corrections BEGIN UPDATE purchases SET merchant_name='Rewritten during context insertion' WHERE id=NEW.purchase_id; END")
				case "reseeded header":
					if kind == "create" {
						execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity BEFORE INSERT ON purchases BEGIN INSERT INTO projection_revisions VALUES('purchase',NEW.id,7); END")
					} else {
						execSQL(t, s.db, "CREATE TRIGGER fail_header_integrity AFTER UPDATE ON purchases BEGIN DELETE FROM current_projections WHERE entity_type='purchase' AND entity_id=NEW.id; DELETE FROM projection_revisions WHERE entity_type='purchase' AND entity_id=NEW.id; INSERT INTO projection_revisions VALUES('purchase',NEW.id,1); END")
					}
				}
				call := func() (domainResult, error) {
					if kind == "noop" {
						return purchases.Patch(t.Context(), fixture.token, "actual-header-integrity-key", fixture.purchase.ID, fixture.purchase.Revision, PurchasePatch{ReceiptNote: &fixture.purchase.ReceiptNote})
					}
					return callPurchaseCommand(t.Context(), fixture, purchases, kind, "actual-header-integrity-key")
				}
				before := purchaseCommandState(t, s)
				if value, err := call(); err == nil || !reflect.ValueOf(value).IsZero() {
					t.Fatal("ignored/rewritten/missing header source or context froze a successful result", kind, failure, err)
				}
				requirePurchaseCommandState(t, s, before)
				execSQL(t, s.db, "DROP TRIGGER fail_header_integrity")
				value, err := call()
				if err != nil {
					t.Fatal("rolled-back original header key/proof/connection could not be explicitly retried", err)
				}
				header, ok := value.(Purchase)
				if !ok || kind == "noop" && header != fixture.purchase || kind != "noop" && header.MerchantName != "UncommittedPurchaseEvidence" {
					t.Fatal("successful retry did not retain exact intended header/no-op fields")
				}
				if kind != "create" && scalar[int](t, s.db, "SELECT COUNT(*) FROM purchase_header_corrections c JOIN mutation_events e ON e.id=c.mutation_event_id WHERE e.operation='patch_purchases_id'") != 1 {
					t.Fatal("successful retry omitted its actual immutable correction context")
				}
			})
		}
	}
}
