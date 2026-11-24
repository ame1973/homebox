package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
)

const maxLocationQuantityGroups = 500

// QuantityGroup never combines different Item base units or rescales a balance.
type QuantityGroup struct {
	BaseUnit     string `json:"base_unit"`
	QuantityBase int64  `json:"quantity_base"`
}

// LocationSummary counts recorded holdings, not verified physical presence.
// Revision is the actual location aggregate proof for HTTP metadata, not an
// additional JSON property.
type LocationSummary struct {
	LocationID        string          `json:"location_id"`
	DirectUnits       int64           `json:"direct_units"`
	SubtreeUnits      int64           `json:"subtree_units"`
	DistinctItemCount int64           `json:"distinct_item_count"`
	DirectQuantities  []QuantityGroup `json:"direct_quantities"`
	SubtreeQuantities []QuantityGroup `json:"subtree_quantities"`
	Revision          int64           `json:"-"`
}

// Summary revalidates a completed session in the same SQLite transaction as
// the source read. It does not publish, repair or infer missing holdings.
func (l *Locations) Summary(ctx context.Context, token, id string) (LocationSummary, error) {
	if ctx == nil || l == nil || l.auth == nil || l.auth.store == nil || l.auth.store.db == nil || !validDomainID(id) {
		return LocationSummary{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return LocationSummary{}, err
	}
	a := l.auth
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return LocationSummary{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return LocationSummary{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return LocationSummary{}, err
	}
	// Activity can run SQLite triggers. Read the summary afterward, then check
	// session authority again before committing the same transaction.
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return LocationSummary{}, err
	}
	value, err := readLocationSummary(ctx, tx, id)
	if err != nil {
		return LocationSummary{}, err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return LocationSummary{}, err
	}
	if err = tx.Commit(); err != nil {
		return LocationSummary{}, err
	}
	return value, nil
}

func readLocationSummary(ctx context.Context, tx *sql.Tx, id string) (LocationSummary, error) {
	location, err := loadLocation(ctx, tx, id)
	if err != nil {
		return LocationSummary{}, err
	}
	// A reachable descendant has one parent. Checking the root's full ancestor
	// chain rejects cycles or deleted ancestors without a depth limit; scanning
	// every descendant below validates its actual source and retained counter.
	if _, err = holdingLocationPath(ctx, tx, id, false); err != nil {
		return LocationSummary{}, err
	}
	rows, err := tx.QueryContext(ctx, subtreeCTE+locationSelect+" JOIN subtree s ON s.id=l.id ORDER BY l.id", id)
	if err != nil {
		return LocationSummary{}, err
	}
	seenRoot := false
	for rows.Next() {
		value, scanErr := scanLocation(rows)
		if scanErr != nil {
			return LocationSummary{}, errors.Join(scanErr, rows.Close())
		}
		if value.ID == id {
			seenRoot = true
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return LocationSummary{}, err
	}
	if !seenRoot {
		return LocationSummary{}, ErrDomainState
	}
	value := LocationSummary{LocationID: id, Revision: location.Revision}
	rows, err = tx.QueryContext(ctx, subtreeCTE+unitSelect+" WHERE u.location_id IN (SELECT id FROM subtree) ORDER BY u.id", id)
	if err != nil {
		return LocationSummary{}, err
	}
	for rows.Next() {
		unit, scanErr := scanUnit(rows)
		if scanErr != nil {
			return LocationSummary{}, errors.Join(scanErr, rows.Close())
		}
		if unit.LocationID == nil {
			return LocationSummary{}, errors.Join(ErrDomainState, rows.Close())
		}
		if unit.Status == "disposed" {
			continue
		}
		if value.SubtreeUnits == maxSafeInteger {
			return LocationSummary{}, errors.Join(ErrDomainState, rows.Close())
		}
		value.SubtreeUnits++
		if *unit.LocationID == id {
			value.DirectUnits++
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return LocationSummary{}, err
	}

	direct, subtree := make(map[string]int64), make(map[string]int64)
	rows, err = tx.QueryContext(ctx, subtreeCTE+stockSelect+" WHERE b.location_id IN (SELECT id FROM subtree) ORDER BY b.item_id,b.location_id", id)
	if err != nil {
		return LocationSummary{}, err
	}
	for rows.Next() {
		stock, scanErr := scanStock(rows)
		if scanErr != nil {
			return LocationSummary{}, errors.Join(scanErr, rows.Close())
		}
		if stock.QuantityBase == 0 {
			continue
		}
		if err = addLocationQuantity(subtree, stock.BaseUnit, stock.QuantityBase); err != nil {
			return LocationSummary{}, errors.Join(err, rows.Close())
		}
		if stock.LocationID == id {
			if err = addLocationQuantity(direct, stock.BaseUnit, stock.QuantityBase); err != nil {
				return LocationSummary{}, errors.Join(err, rows.Close())
			}
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return LocationSummary{}, err
	}
	value.DirectQuantities = orderedLocationQuantities(direct)
	value.SubtreeQuantities = orderedLocationQuantities(subtree)
	err = tx.QueryRowContext(ctx, subtreeCTE+`SELECT COUNT(*) FROM (
 SELECT item_id FROM item_units WHERE location_id IN (SELECT id FROM subtree) AND status<>'disposed'
 UNION SELECT item_id FROM stock_balances WHERE location_id IN (SELECT id FROM subtree) AND quantity_base>0
)`, id).Scan(&value.DistinctItemCount)
	if err != nil {
		return LocationSummary{}, err
	}
	if value.DistinctItemCount < 0 || value.DistinctItemCount > maxSafeInteger {
		return LocationSummary{}, ErrDomainState
	}
	return value, nil
}

func addLocationQuantity(groups map[string]int64, unit string, quantity int64) error {
	previous, exists := groups[unit]
	if quantity <= 0 || previous > maxSafeInteger-quantity || !exists && len(groups) >= maxLocationQuantityGroups {
		return ErrDomainState
	}
	groups[unit] = previous + quantity
	return nil
}

func orderedLocationQuantities(groups map[string]int64) []QuantityGroup {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]QuantityGroup, 0, len(keys))
	for _, key := range keys {
		result = append(result, QuantityGroup{BaseUnit: key, QuantityBase: groups[key]})
	}
	return result
}
