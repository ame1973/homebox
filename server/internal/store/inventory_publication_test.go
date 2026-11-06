package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestInventoryFinalEventCorruptionRollsBackRealStockAndEveryFrozenEffect(t *testing.T) {
	for _, corruption := range []struct{ name, time, quantity, note string }{
		{"unsupported instant", "'2026'", "NEW.quantity_base", "NEW.note"},
		{"unsafe quantity", "NEW.occurred_at", fmt.Sprint(maxSafeInteger + 1), "NEW.note"},
		{"invalid UTF", "NEW.occurred_at", "NEW.quantity_base", "CAST(x'ff' AS TEXT)"},
		{"native NUL bound", "NEW.occurred_at", "NEW.quantity_base", "char(0)||printf('%010000d',0)"},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			fixture, inventory := inventoryFixture(t)
			// Replace a producer insert with corrupt final source. The marker keeps
			// this test trigger finite even if recursive triggers are enabled.
			execSQL(t, fixture.store.db, fmt.Sprintf(`CREATE TRIGGER corrupt_final_stock_event BEFORE INSERT ON inventory_events
 WHEN NEW.reason<>'injected_corruption' BEGIN
 INSERT INTO inventory_events(id,item_id,from_location_id,to_location_id,action,quantity_base,occurred_at,note,reason,mutation_event_id)
 VALUES(NEW.id,NEW.item_id,NEW.from_location_id,NEW.to_location_id,NEW.action,%s,%s,%s,'injected_corruption',NEW.mutation_event_id);
 SELECT RAISE(IGNORE); END`, corruption.quantity, corruption.time, corruption.note))
			before := domainState(t, fixture.store)
			input := StockTransfer{ItemID: fixture.quantity.ID, SourceLocationID: "source", TargetLocationID: "root", QuantityBase: 10}
			if reply, err := inventory.Transfer(t.Context(), fixture.token, "corrupt-final-event-original", fixture.quantity.Revision, input); !errors.Is(err, ErrDomainState) || reply != (Stock{}) {
				t.Fatal("corrupt final movement bypassed source publication validation", err)
			}
			requireDomainState(t, fixture.store, before)
			execSQL(t, fixture.store.db, "DROP TRIGGER corrupt_final_stock_event")
			if _, err := inventory.Transfer(t.Context(), fixture.token, "corrupt-final-event-original", fixture.quantity.Revision, input); err != nil {
				t.Fatal("failed final builder retained effects or original key", err)
			}
		})
	}
}
