package store

import (
	"context"
	"database/sql"
	"errors"

	"homebox/internal/barcode"
)

// BarcodeMatch identifies a product kind, not a unique physical unit.
type BarcodeMatch struct {
	ItemID          string `json:"item_id"`
	BarcodeID       string `json:"barcode_id"`
	Symbology       string `json:"symbology"`
	NormalizedValue string `json:"normalized_value"`
}

type BarcodeMatches struct {
	Matches    []BarcodeMatch `json:"matches"`
	NextCursor *string        `json:"next_cursor"`
}

// SearchBarcodes reads one live page of exact retained normalized values.
// An empty symbology searches every supported namespace without conversion.
func (catalog *Catalog) SearchBarcodes(ctx context.Context, token, value, symbology, cursor string, limit int) (BarcodeMatches, error) {
	if !authString(value, 1, 512) || symbology != "" && !barcode.ValidSymbology(symbology) {
		return BarcodeMatches{}, ErrDomainInput
	}
	// The optional namespace cannot contain NUL, so this scope is unambiguous even
	// when the exact value contains controls. The cursor body contains no filter.
	purpose := "product-barcode-search-cursor-v1\x00" + symbology + "\x00" + value
	page, err := readMetadataPage(ctx, catalog.auth, token, cursor, limit, purpose, func(ctx context.Context, tx *sql.Tx, afterID string, size int) (metadataPage[BarcodeMatch], error) {
		query := barcodeSelect + " WHERE b.normalized_value=?"
		args := []any{value}
		if symbology != "" {
			query += " AND b.symbology=?"
			args = append(args, symbology)
		}
		query += " AND b.id>? ORDER BY b.id LIMIT ?"
		args = append(args, afterID, size+1)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return metadataPage[BarcodeMatch]{}, err
		}
		defer rows.Close()
		result := metadataPage[BarcodeMatch]{Data: make([]BarcodeMatch, 0)}
		for rows.Next() {
			row, err := scanBarcode(rows)
			if err != nil {
				return metadataPage[BarcodeMatch]{}, err
			}
			if row.NormalizedValue != value || symbology != "" && row.Symbology != symbology {
				return metadataPage[BarcodeMatch]{}, ErrDomainState
			}
			if _, err = loadItem(ctx, tx, row.ItemID); err != nil {
				if errors.Is(err, ErrNotFound) {
					return metadataPage[BarcodeMatch]{}, ErrDomainState
				}
				return metadataPage[BarcodeMatch]{}, err
			}
			result.Data = append(result.Data, BarcodeMatch{ItemID: row.ItemID, BarcodeID: row.ID,
				Symbology: row.Symbology, NormalizedValue: row.NormalizedValue})
		}
		if err = rows.Err(); err != nil {
			return metadataPage[BarcodeMatch]{}, err
		}
		if err = rows.Close(); err != nil {
			return metadataPage[BarcodeMatch]{}, err
		}
		if len(result.Data) > size {
			result.Data = result.Data[:size]
			result.NextID = result.Data[size-1].BarcodeID
		}
		return result, nil
	})
	if err != nil {
		return BarcodeMatches{}, err
	}
	return BarcodeMatches{Matches: page.Data, NextCursor: page.NextCursor}, nil
}
