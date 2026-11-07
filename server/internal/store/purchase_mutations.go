package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"homebox/internal/currency"
)

var (
	ErrPurchaseCurrency       = errors.New("currency is unsupported for new money or conflicts with allocated financial source")
	ErrPurchaseCurrencyLocked = errors.New("purchase currency cannot change after allocation")
	ErrPurchaseAssetBasis     = errors.New("manual asset acquisition cannot also use a purchase allocation")
)

type PurchaseCreate struct {
	MerchantName string `json:"merchant_name"`
	PurchasedAt  string `json:"purchased_at"`
	Currency     string `json:"currency"`
	ReceiptNote  string `json:"receipt_note"`
}

type PurchasePatch struct {
	MerchantName *string `json:"merchant_name,omitempty"`
	PurchasedAt  *string `json:"purchased_at,omitempty"`
	Currency     *string `json:"currency,omitempty"`
	ReceiptNote  *string `json:"receipt_note,omitempty"`
}

// ItemRevision binds the reviewed measurement independently of Purchase If-Match.
// The HTTP reader must separately require explicit nullable item_unit_id presence.
type PurchaseLineCreate struct {
	ItemID       string  `json:"item_id"`
	ItemRevision int64   `json:"item_revision"`
	ItemUnitID   *string `json:"item_unit_id"`
	QuantityBase int64   `json:"quantity_base"`
	PaidMinor    int64   `json:"paid_minor"`
	Note         string  `json:"note"`
}

// Purchases owns actual purchase transactions; HTTP still owns transport,
// Origin/CSRF/client-version/admission. No physical receipt or AI worker is needed.
type Purchases struct{ auth *Auth }

func NewPurchases(a *Auth) (*Purchases, error) {
	if a == nil || a.store == nil {
		return nil, ErrDomainInput
	}
	return a.store.NewPurchases(a)
}
func (s *Store) NewPurchases(a *Auth) (*Purchases, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &Purchases{auth: a}, nil
}
func (p *Purchases) Get(ctx context.Context, token, id string) (Purchase, error) {
	if !validDomainID(id) {
		return Purchase{}, ErrDomainInput
	}
	return readDomain(ctx, p.auth, token, func(tx *sql.Tx) (Purchase, error) { return loadPurchase(ctx, tx, id) })
}

func (p *Purchases) Create(ctx context.Context, token, key string, input PurchaseCreate) (Purchase, error) {
	value := PurchaseData{ID: "validation", MerchantName: input.MerchantName, PurchasedAt: input.PurchasedAt, Currency: input.Currency, ReceiptNote: input.ReceiptNote}
	if (Purchase{value, 1}).validateDomainResult() != nil {
		return Purchase{}, ErrDomainInput
	}
	return runDomainMutation(ctx, p.auth, token, key, "post_purchases", input, func(m *mutationTx) (Purchase, error) {
		if _, ok := currency.MinorUnits(input.Currency); !ok {
			return Purchase{}, ErrPurchaseCurrency
		}
		id, err := newDomainID()
		if err != nil {
			return Purchase{}, err
		}
		instant, _ := time.Parse(time.RFC3339Nano, input.PurchasedAt)
		_, err = m.tx.ExecContext(ctx, "INSERT INTO purchases(id,merchant_name,purchased_at,currency,receipt_note,created_at) VALUES(?,?,?,?,?,?)", id, input.MerchantName, stamp(instant), input.Currency, input.ReceiptNote, stamp(m.now))
		if err != nil {
			return Purchase{}, err
		}
		revision, err := m.touch("purchase", id, false)
		if err != nil {
			return Purchase{}, err
		}
		if revision != 1 {
			return Purchase{}, ErrDomainState
		}
		return loadPurchase(ctx, m.tx, id)
	})
}

func snapshotPurchaseString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (p *Purchases) Patch(ctx context.Context, token, key, id string, revision int64, input PurchasePatch) (Purchase, error) {
	input = PurchasePatch{snapshotPurchaseString(input.MerchantName), snapshotPurchaseString(input.PurchasedAt), snapshotPurchaseString(input.Currency), snapshotPurchaseString(input.ReceiptNote)}
	if !validDomainID(id) || input == (PurchasePatch{}) ||
		(input.MerchantName != nil && !authString(*input.MerchantName, 1, 300)) ||
		(input.PurchasedAt != nil && !validPurchaseInstant(*input.PurchasedAt)) ||
		(input.Currency != nil && !validCurrencyCode(*input.Currency)) ||
		(input.ReceiptNote != nil && !authString(*input.ReceiptNote, 0, 10000)) {
		return Purchase{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return Purchase{}, err
	}
	request := struct {
		ID       string        `json:"id"`
		Revision int64         `json:"revision"`
		Input    PurchasePatch `json:"input"`
	}{id, revision, input}
	return runDomainMutation(ctx, p.auth, token, key, "patch_purchases_id", request, func(m *mutationTx) (Purchase, error) {
		before, err := loadPurchase(ctx, m.tx, id)
		if err != nil {
			return Purchase{}, err
		}
		if before.Revision != revision {
			return Purchase{}, ErrRevisionConflict
		}
		after := before
		if input.MerchantName != nil {
			after.MerchantName = *input.MerchantName
		}
		if input.ReceiptNote != nil {
			after.ReceiptNote = *input.ReceiptNote
		}
		if input.Currency != nil {
			after.Currency = *input.Currency
		}
		if input.PurchasedAt != nil {
			instant, _ := time.Parse(time.RFC3339Nano, *input.PurchasedAt)
			original, _ := time.Parse(time.RFC3339Nano, before.PurchasedAt)
			if !instant.Equal(original) {
				after.PurchasedAt = stamp(instant)
			}
		}
		if after.Currency != before.Currency {
			var allocated bool
			if err = m.tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM purchase_lines WHERE purchase_id=?)", id).Scan(&allocated); err != nil {
				return Purchase{}, err
			}
			if allocated {
				return Purchase{}, ErrPurchaseCurrencyLocked
			}
			if _, ok := currency.MinorUnits(after.Currency); !ok {
				return Purchase{}, ErrPurchaseCurrency
			}
		}
		if after.PurchaseData != before.PurchaseData {
			items, err := m.purchaseItems(id)
			if err != nil {
				return Purchase{}, err
			}
			if _, err = m.tx.ExecContext(ctx, "UPDATE purchases SET merchant_name=?,purchased_at=?,currency=?,receipt_note=? WHERE id=?", after.MerchantName, after.PurchasedAt, after.Currency, after.ReceiptNote, id); err != nil {
				return Purchase{}, err
			}
			if _, err = m.touch("purchase", id, false); err != nil {
				return Purchase{}, err
			}
			for _, item := range items {
				if err = m.touchItemSearch(item, true); err != nil {
					return Purchase{}, err
				}
			}
			after, err = loadPurchase(ctx, m.tx, id)
			if err != nil {
				return Purchase{}, err
			}
		}
		if err = m.recordPurchaseCorrection(before, after, input); err != nil {
			return Purchase{}, err
		}
		return after, nil
	})
}

// Read the entire actual owner set before changing any source or dependency.
func (m *mutationTx) purchaseItems(id string) ([]string, error) {
	rows, err := m.tx.QueryContext(m.ctx, purchaseLineSelect+" WHERE l.purchase_id=? ORDER BY l.id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []string
	seen := make(map[string]bool)
	for rows.Next() {
		line, err := scanPurchaseLine(rows)
		if err != nil {
			return nil, err
		}
		if err = validatePurchaseLineAuthority(m.ctx, m.tx, line); err != nil {
			return nil, err
		}
		if !seen[line.ItemID] {
			seen[line.ItemID] = true
			items = append(items, line.ItemID)
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return items, nil
}

func (m *mutationTx) recordPurchaseCorrection(before, after Purchase, input PurchasePatch) error {
	context, err := json.Marshal(struct {
		Before Purchase      `json:"before"`
		After  Purchase      `json:"after"`
		Input  PurchasePatch `json:"input"`
	}{before, after, input})
	if err != nil {
		return err
	}
	_, err = m.tx.ExecContext(m.ctx, "INSERT INTO purchase_header_corrections(mutation_event_id,purchase_id,context_json) VALUES(?,?,?)", m.eventID, before.ID, string(context))
	return err
}

func (p *Purchases) CreateLine(ctx context.Context, token, key, purchaseID string, revision int64, input PurchaseLineCreate) (PurchaseLine, error) {
	input.ItemUnitID = snapshotPurchaseString(input.ItemUnitID)
	if !validDomainID(purchaseID) || !validDomainID(input.ItemID) || input.ItemRevision < 1 || input.ItemRevision > maxSafeInteger ||
		(input.ItemUnitID != nil && !validDomainID(*input.ItemUnitID)) || input.QuantityBase < 1 || input.QuantityBase > maxSafeInteger ||
		input.PaidMinor < 0 || input.PaidMinor > maxSafeInteger || !authString(input.Note, 0, 10000) || (input.ItemUnitID != nil && input.QuantityBase != 1) {
		return PurchaseLine{}, ErrDomainInput
	}
	if err := validExpectedRevision(revision); err != nil {
		return PurchaseLine{}, err
	}
	request := struct {
		ID       string             `json:"id"`
		Revision int64              `json:"revision"`
		Input    PurchaseLineCreate `json:"input"`
	}{purchaseID, revision, input}
	return runDomainMutation(ctx, p.auth, token, key, "post_purchases_id_lines", request, func(m *mutationTx) (PurchaseLine, error) {
		purchase, err := loadPurchase(ctx, m.tx, purchaseID)
		if err != nil {
			return PurchaseLine{}, err
		}
		if purchase.Revision != revision {
			return PurchaseLine{}, ErrRevisionConflict
		}
		item, err := itemAtRevision(ctx, m.tx, input.ItemID, input.ItemRevision)
		if err != nil {
			return PurchaseLine{}, err
		}
		if _, ok := currency.MinorUnits(purchase.Currency); !ok {
			return PurchaseLine{}, ErrPurchaseCurrency
		}
		var asset *Asset
		if input.ItemUnitID != nil {
			if item.TrackingMode != "individual" {
				return PurchaseLine{}, ErrUnitTracking
			}
			unit, err := loadUnit(ctx, m.tx, *input.ItemUnitID)
			if err != nil {
				return PurchaseLine{}, err
			}
			if unit.ItemID != item.ID {
				return PurchaseLine{}, ErrDomainInput
			}
			asset, err = m.purchaseAsset(purchase, unit)
			if err != nil {
				return PurchaseLine{}, err
			}
		}
		id, err := newDomainID()
		if err != nil {
			return PurchaseLine{}, err
		}
		err = requireDomainRow(m.tx.ExecContext(ctx, `INSERT INTO purchase_lines(id,purchase_id,item_id,item_unit_id,quantity_base,paid_minor,note,created_at,mutation_event_id) VALUES(?,?,?,?,?,?,?,?,?)`, id, purchaseID, input.ItemID, input.ItemUnitID, input.QuantityBase, input.PaidMinor, input.Note, stamp(m.now), m.eventID))
		if err != nil {
			return PurchaseLine{}, err
		}
		lineRevision, err := m.touch("purchase_line", id, false)
		if err != nil {
			return PurchaseLine{}, err
		}
		if lineRevision != 1 {
			return PurchaseLine{}, ErrDomainState
		}
		if _, err = m.touch("purchase", purchaseID, false); err != nil {
			return PurchaseLine{}, err
		}
		if err = m.touchItemSearch(input.ItemID, true); err != nil {
			return PurchaseLine{}, err
		}
		if asset != nil {
			if err = m.finishAssetEvent(*asset); err != nil {
				return PurchaseLine{}, err
			}
			stored, err := loadAsset(ctx, m.tx, asset.ItemUnitID)
			if err != nil {
				return PurchaseLine{}, err
			}
			if stored.Revision != asset.Revision+1 || !reflect.DeepEqual(stored.AssetData, asset.AssetData) {
				return PurchaseLine{}, ErrDomainState
			}
		}
		stored, err := loadPurchaseLine(ctx, m.tx, id)
		if err != nil {
			return PurchaseLine{}, err
		}
		expected := PurchaseLineData{id, purchaseID, input.ItemID, input.ItemUnitID, input.QuantityBase, input.PaidMinor, input.Note}
		if stored.Revision != 1 || !reflect.DeepEqual(stored.PurchaseLineData, expected) {
			return PurchaseLine{}, ErrDomainState
		}
		return stored, nil
	})
}

// The original Item proof freezes profile changes too: every financial producer
// advances that aggregate. Never opt in, change basis/currency or repair authority.
func (m *mutationTx) purchaseAsset(purchase Purchase, unit Unit) (*Asset, error) {
	asset, err := loadAsset(m.ctx, m.tx, unit.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if asset.CostBasis == "manual" {
		return nil, ErrPurchaseAssetBasis
	}
	if asset.Currency != purchase.Currency {
		return nil, ErrPurchaseCurrency
	}
	return &asset, nil
}
