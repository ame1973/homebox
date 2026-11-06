package store

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrStockTracking     = errors.New("stock commands require a quantity-tracked item")
	ErrStockInsufficient = errors.New("source stock is insufficient")
	ErrStockOverflow     = errors.New("target stock would exceed the safe base quantity")
)

type StockReceive struct {
	ItemID           string `json:"item_id"`
	TargetLocationID string `json:"target_location_id"`
	QuantityBase     int64  `json:"quantity_base"`
	Note             string `json:"note"`
}

type StockConsume struct {
	ItemID           string `json:"item_id"`
	SourceLocationID string `json:"source_location_id"`
	QuantityBase     int64  `json:"quantity_base"`
	Note             string `json:"note"`
}

type StockTransfer struct {
	ItemID           string `json:"item_id"`
	SourceLocationID string `json:"source_location_id"`
	TargetLocationID string `json:"target_location_id"`
	QuantityBase     int64  `json:"quantity_base"`
	Note             string `json:"note"`
}

type StockAdjust struct {
	ItemID           string `json:"item_id"`
	TargetLocationID string `json:"target_location_id"`
	QuantityBase     int64  `json:"quantity_base"`
	Reason           string `json:"reason"`
	Note             string `json:"note"`
}

// Inventory is an authoritative physical transaction service, not middleware.
// Callers still own Origin/CSRF/version/admission and explicit foreground review.
type Inventory struct{ auth *Auth }

func NewInventory(a *Auth) (*Inventory, error) {
	if a == nil || a.store == nil {
		return nil, ErrDomainInput
	}
	return a.store.NewInventory(a)
}

func (s *Store) NewInventory(a *Auth) (*Inventory, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &Inventory{auth: a}, nil
}

func (inventory *Inventory) Receive(ctx context.Context, token, key string, revision int64, input StockReceive) (Stock, error) {
	return inventory.mutateStock(ctx, token, key, revision, input, stockCommand{action: "receive", itemID: input.ItemID,
		targetID: input.TargetLocationID, quantity: input.QuantityBase, note: input.Note})
}

func (inventory *Inventory) Consume(ctx context.Context, token, key string, revision int64, input StockConsume) (Stock, error) {
	return inventory.mutateStock(ctx, token, key, revision, input, stockCommand{action: "consume", itemID: input.ItemID,
		sourceID: input.SourceLocationID, quantity: input.QuantityBase, note: input.Note})
}

func (inventory *Inventory) Transfer(ctx context.Context, token, key string, revision int64, input StockTransfer) (Stock, error) {
	return inventory.mutateStock(ctx, token, key, revision, input, stockCommand{action: "transfer", itemID: input.ItemID,
		sourceID: input.SourceLocationID, targetID: input.TargetLocationID, quantity: input.QuantityBase, note: input.Note})
}

func (inventory *Inventory) Adjust(ctx context.Context, token, key string, revision int64, input StockAdjust) (Stock, error) {
	return inventory.mutateStock(ctx, token, key, revision, input, stockCommand{action: "adjust", itemID: input.ItemID,
		targetID: input.TargetLocationID, quantity: input.QuantityBase, note: input.Note, reason: input.Reason})
}

type stockCommand struct {
	action, itemID, sourceID, targetID string
	quantity                           int64
	note, reason                       string
}

func (inventory *Inventory) mutateStock(ctx context.Context, token, key string, revision int64, input any, command stockCommand) (Stock, error) {
	if !validDomainID(command.itemID) || !authString(command.note, 0, 10000) || command.quantity < 0 || command.quantity > maxSafeInteger {
		return Stock{}, ErrDomainInput
	}
	switch command.action {
	case "receive", "adjust":
		if !validDomainID(command.targetID) {
			return Stock{}, ErrDomainInput
		}
	case "consume":
		if !validDomainID(command.sourceID) {
			return Stock{}, ErrDomainInput
		}
	case "transfer":
		if !validDomainID(command.sourceID) || !validDomainID(command.targetID) || command.sourceID == command.targetID {
			return Stock{}, ErrDomainInput
		}
	default:
		return Stock{}, ErrDomainInput
	}
	if command.action == "adjust" {
		if !authString(command.reason, 1, 10000) {
			return Stock{}, ErrDomainInput
		}
	} else if command.quantity == 0 {
		return Stock{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Stock{}, err
	}
	request := struct {
		Revision int64 `json:"revision"`
		Input    any   `json:"input"`
	}{revision, input}
	return runDomainMutation(ctx, inventory.auth, token, key, "post_stock_"+command.action, request, func(m *mutationTx) (Stock, error) {
		return m.changeStock(command, revision)
	})
}

func (m *mutationTx) changeStock(command stockCommand, revision int64) (Stock, error) {
	item, err := itemAtRevision(m.ctx, m.tx, command.itemID, revision)
	if err != nil {
		return Stock{}, err
	}
	if item.TrackingMode != "quantity" {
		return Stock{}, ErrStockTracking
	}
	m.note, m.reason = command.note, command.reason
	var paths []string
	var source, target Stock
	var targetExists bool
	if command.sourceID != "" {
		path, err := stockLocationPath(m.ctx, m.tx, command.sourceID, false)
		if err != nil {
			return Stock{}, err
		}
		paths = append(paths, path...)
		source, err = loadStock(m.ctx, m.tx, item.ID, command.sourceID)
		if errors.Is(err, ErrNotFound) {
			return Stock{}, ErrStockInsufficient
		}
		if err != nil {
			return Stock{}, err
		}
		if source.QuantityBase < command.quantity {
			return Stock{}, ErrStockInsufficient
		}
	}
	if command.targetID != "" {
		target, err = loadStock(m.ctx, m.tx, item.ID, command.targetID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Stock{}, err
		}
		targetExists = err == nil
		active := command.action != "adjust" || !targetExists || command.quantity > target.QuantityBase
		path, err := stockLocationPath(m.ctx, m.tx, command.targetID, active)
		if err != nil {
			return Stock{}, err
		}
		paths = append(paths, path...)
	}
	// A missing pair is not an existing zero balance: an explicit zero adjustment
	// creates retained source/history. Equal existing quantities only get a receipt.
	if command.action == "adjust" && targetExists && target.QuantityBase == command.quantity {
		return target, nil
	}

	quantity, nextTarget := command.quantity, command.quantity
	var from, to *string
	resultLocation := command.targetID
	if command.action == "consume" {
		quantity = -quantity
		resultLocation = command.sourceID
	} else if command.action == "adjust" {
		quantity = nextTarget - target.QuantityBase
	} else {
		if target.QuantityBase > maxSafeInteger-command.quantity {
			return Stock{}, ErrStockOverflow
		}
		nextTarget += target.QuantityBase
	}
	// All arithmetic and path checks precede either leg of a transfer.
	if command.sourceID != "" {
		if err = m.setStock(item.ID, command.sourceID, source.QuantityBase-command.quantity, true); err != nil {
			return Stock{}, err
		}
		from = &command.sourceID
	}
	if command.targetID != "" {
		if err = m.setStock(item.ID, command.targetID, nextTarget, targetExists); err != nil {
			return Stock{}, err
		}
		to = &command.targetID
		if quantity < 0 {
			from, to = to, nil
		}
	}
	if _, err = m.touch("item", item.ID, false); err != nil {
		return Stock{}, err
	}
	if _, err = m.tx.ExecContext(m.ctx, "UPDATE items SET updated_at=? WHERE id=?", stamp(m.now), item.ID); err != nil {
		return Stock{}, err
	}
	for _, id := range paths {
		if _, err = m.touch("location", id, false); err != nil {
			return Stock{}, err
		}
	}
	if err = m.appendStockMovement(item.ID, command.action, from, to, quantity, command.note, command.reason); err != nil {
		return Stock{}, err
	}
	return loadStock(m.ctx, m.tx, item.ID, resultLocation)
}

func (m *mutationTx) setStock(itemID, locationID string, quantity int64, exists bool) error {
	var err error
	if exists {
		_, err = m.tx.ExecContext(m.ctx, "UPDATE stock_balances SET quantity_base=?,updated_at=? WHERE item_id=? AND location_id=?", quantity, stamp(m.now), itemID, locationID)
	} else {
		_, err = m.tx.ExecContext(m.ctx, "INSERT INTO stock_balances(item_id,location_id,quantity_base,updated_at) VALUES(?,?,?,?)", itemID, locationID, quantity, stamp(m.now))
	}
	if err != nil {
		return err
	}
	_, err = m.touch("stock", projectionPair(itemID, locationID), false)
	return err
}

// Validate every ancestor's source and retained authority before touching it.
// An outflow may leave an archived path; new/increased holdings require active
// destinations. A deleted/missing ancestor or a cycle is corruption, not a root.
func stockLocationPath(ctx context.Context, tx *sql.Tx, id string, active bool) ([]string, error) {
	var path []string
	seen := make(map[string]bool)
	for current := id; current != ""; {
		if seen[current] {
			return nil, ErrDomainState
		}
		seen[current] = true
		value, err := loadLocation(ctx, tx, current)
		if errors.Is(err, ErrNotFound) && current != id {
			return nil, ErrDomainState
		}
		if err != nil {
			return nil, err
		}
		if active && value.Status != "active" {
			return nil, ErrLocationArchived
		}
		path = append(path, current)
		if value.ParentID == nil {
			break
		}
		current = *value.ParentID
	}
	return path, nil
}
