package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

const locationContentsCursorBytes = 4096

// LocationContents is one bounded page of recorded Units, Stock and child
// locations. Revision is the aggregate Location proof for this live read.
type LocationContents struct {
	LocationID   string     `json:"location_id"`
	Scope        string     `json:"scope"`
	Units        []Unit     `json:"units"`
	Stock        []Stock    `json:"stock"`
	SubLocations []Location `json:"sub_locations"`
	NextCursor   *string    `json:"next_cursor"`
	Revision     int64      `json:"-"`
}

type locationContentsPosition struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	LocationID string `json:"location_id,omitempty"`
}

type locationContentsRow struct {
	position locationContentsPosition
	unit     *Unit
	stock    *Stock
	location *Location
}

func validLocationContentsPosition(value string) bool {
	if len(value) > 2*128*6+128 {
		return false
	}
	var position locationContentsPosition
	if json.Unmarshal([]byte(value), &position) != nil || !validDomainID(position.ID) {
		return false
	}
	switch position.Kind {
	case "unit", "location":
		if position.LocationID != "" {
			return false
		}
	case "stock":
		if !validDomainID(position.LocationID) {
			return false
		}
	default:
		return false
	}
	encoded, err := json.Marshal(position)
	return err == nil && bytes.Equal(encoded, []byte(value))
}

// PageContents reads one live, bounded page. A continuation is scoped to the
// actual Location aggregate revision, not a credential or frozen snapshot.
func (l *Locations) PageContents(ctx context.Context, token, id, scope, cursor string, limit int) (LocationContents, error) {
	if ctx == nil || l == nil || l.auth == nil || l.auth.store == nil || l.auth.store.db == nil ||
		!validDomainID(id) || scope != "direct" && scope != "subtree" || limit < 1 || limit > 100 || len(cursor) > locationContentsCursorBytes {
		return LocationContents{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return LocationContents{}, err
	}
	a := l.auth
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return LocationContents{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return LocationContents{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return LocationContents{}, err
	}
	// Session activity can run triggers; read source and continuation scope only
	// afterward, then recheck authority before the same transaction commits.
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return LocationContents{}, err
	}
	location, err := validatedLocationRoot(ctx, tx, id)
	if err != nil {
		return LocationContents{}, err
	}
	if scope == "subtree" {
		if err = validateLocationSubtree(ctx, tx, id); err != nil {
			return LocationContents{}, err
		}
	}
	purpose := "location-contents-cursor-v1\x00" + projectionPair(id, scope) + ":" + strconv.FormatInt(location.Revision, 10)
	page, err := readMetadataIdentityPageTx(ctx, tx, a, token, cursor, limit, purpose, locationContentsCursorBytes, validLocationContentsPosition,
		func(ctx context.Context, tx *sql.Tx, after string, size int) (metadataPage[locationContentsRow], error) {
			return listLocationContents(ctx, tx, id, scope, after, size)
		})
	if err != nil {
		return LocationContents{}, err
	}
	result := LocationContents{LocationID: id, Scope: scope, Units: make([]Unit, 0), Stock: make([]Stock, 0),
		SubLocations: make([]Location, 0), NextCursor: page.NextCursor, Revision: location.Revision}
	for _, row := range page.Data {
		switch row.position.Kind {
		case "unit":
			result.Units = append(result.Units, *row.unit)
		case "stock":
			result.Stock = append(result.Stock, *row.stock)
		case "location":
			result.SubLocations = append(result.SubLocations, *row.location)
		}
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return LocationContents{}, err
	}
	if err = tx.Commit(); err != nil {
		return LocationContents{}, err
	}
	return result, nil
}

func listLocationContents(ctx context.Context, tx *sql.Tx, id, scope, after string, limit int) (metadataPage[locationContentsRow], error) {
	var position locationContentsPosition
	if after != "" && (!validLocationContentsPosition(after) || json.Unmarshal([]byte(after), &position) != nil) {
		return metadataPage[locationContentsRow]{}, ErrInvalidCursor
	}
	page := metadataPage[locationContentsRow]{Data: make([]locationContentsRow, 0, limit)}
	if position.Kind == "" || position.Kind == "unit" {
		query := unitSelect + " WHERE u.location_id=? AND u.id>? ORDER BY u.id LIMIT ?"
		if scope == "subtree" {
			query = subtreeCTE + unitSelect + " WHERE u.location_id IN (SELECT id FROM subtree) AND u.id>? ORDER BY u.id LIMIT ?"
		}
		rows, err := tx.QueryContext(ctx, query, id, position.ID, limit-len(page.Data)+1)
		if err != nil {
			return metadataPage[locationContentsRow]{}, err
		}
		done, err := appendLocationContentsRows(rows, limit, &page, scanUnit, func(unit Unit) locationContentsRow {
			return locationContentsRow{position: locationContentsPosition{Kind: "unit", ID: unit.ID}, unit: &unit}
		})
		if err != nil || done {
			return page, err
		}
	}
	if position.Kind == "" || position.Kind == "unit" || position.Kind == "stock" {
		afterItem, afterLocation := "", ""
		if position.Kind == "stock" {
			afterItem, afterLocation = position.ID, position.LocationID
		}
		query := stockSelect + " WHERE b.location_id=? AND (b.item_id,b.location_id)>(?,?) ORDER BY b.item_id,b.location_id LIMIT ?"
		if scope == "subtree" {
			query = subtreeCTE + stockSelect + " WHERE b.location_id IN (SELECT id FROM subtree) AND (b.item_id,b.location_id)>(?,?) ORDER BY b.item_id,b.location_id LIMIT ?"
		}
		rows, err := tx.QueryContext(ctx, query, id, afterItem, afterLocation, limit-len(page.Data)+1)
		if err != nil {
			return metadataPage[locationContentsRow]{}, err
		}
		done, err := appendLocationContentsRows(rows, limit, &page, scanStock, func(stock Stock) locationContentsRow {
			return locationContentsRow{position: locationContentsPosition{Kind: "stock", ID: stock.ItemID, LocationID: stock.LocationID}, stock: &stock}
		})
		if err != nil || done {
			return page, err
		}
	}
	afterLocation := ""
	if position.Kind == "location" {
		afterLocation = position.ID
	}
	query, args := locationSelect+" WHERE l.parent_id=? AND l.deleted_at IS NULL AND l.id>? ORDER BY l.id LIMIT ?", []any{id, afterLocation, limit - len(page.Data) + 1}
	if scope == "subtree" {
		query = subtreeCTE + locationSelect + " JOIN subtree s ON s.id=l.id WHERE l.id<>? AND l.id>? ORDER BY l.id LIMIT ?"
		args = []any{id, id, afterLocation, limit - len(page.Data) + 1}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return metadataPage[locationContentsRow]{}, err
	}
	_, err = appendLocationContentsRows(rows, limit, &page, scanLocation, func(location Location) locationContentsRow {
		return locationContentsRow{position: locationContentsPosition{Kind: "location", ID: location.ID}, location: &location}
	})
	return page, err
}

func appendLocationContentsRows[T any](rows *sql.Rows, limit int, page *metadataPage[locationContentsRow], scan func(rowScanner) (T, error), row func(T) locationContentsRow) (bool, error) {
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return false, errors.Join(err, rows.Close())
		}
		if len(page.Data) == limit {
			position, err := json.Marshal(page.Data[len(page.Data)-1].position)
			if err != nil {
				return false, errors.Join(err, rows.Close())
			}
			page.NextID = string(position)
			return true, rows.Close()
		}
		page.Data = append(page.Data, row(value))
	}
	return false, errors.Join(rows.Err(), rows.Close())
}
