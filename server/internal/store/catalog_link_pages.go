package store

import (
	"context"
	"database/sql"
)

type BarcodeList = metadataList[Barcode]
type AliasList = metadataList[Alias]
type AttributeList = metadataList[Attribute]
type ItemTagList = metadataList[Tag]

func (catalog *Catalog) PageBarcodes(ctx context.Context, token, itemID, cursor string, limit int) (BarcodeList, error) {
	return readCatalogLinksPage(ctx, catalog.auth, token, itemID, cursor, limit, "barcodes",
		barcodeSelect+" WHERE b.item_id=? AND b.id>? ORDER BY b.id LIMIT ?", scanBarcode, func(value Barcode) string { return value.ID })
}

func (catalog *Catalog) PageAliases(ctx context.Context, token, itemID, cursor string, limit int) (AliasList, error) {
	return readCatalogLinksPage(ctx, catalog.auth, token, itemID, cursor, limit, "aliases",
		aliasSelect+" WHERE a.item_id=? AND a.id>? ORDER BY a.id LIMIT ?", scanAlias, func(value Alias) string { return value.ID })
}

func (catalog *Catalog) PageAttributes(ctx context.Context, token, itemID, cursor string, limit int) (AttributeList, error) {
	return readCatalogLinksPage(ctx, catalog.auth, token, itemID, cursor, limit, "attributes",
		attributeSelect+" WHERE a.item_id=? AND a.id>? ORDER BY a.id LIMIT ?", scanAttribute, func(value Attribute) string { return value.ID })
}

func (catalog *Catalog) PageTags(ctx context.Context, token, itemID, cursor string, limit int) (ItemTagList, error) {
	return readCatalogLinksPage(ctx, catalog.auth, token, itemID, cursor, limit, "tags",
		tagSelect+" JOIN item_tags l ON l.tag_id=t.id WHERE l.item_id=? AND t.id>? ORDER BY t.id LIMIT ?", scanTag, func(value Tag) string { return value.ID })
}

// Reuse the existing authorized live-list cursor boundary without nested public
// reads or changing any location/category/global-tag continuation spelling.
func readCatalogLinksPage[T any](ctx context.Context, auth *Auth, token, itemID, cursor string, limit int, kind, query string,
	scan func(rowScanner) (T, error), identity func(T) string,
) (metadataList[T], error) {
	if !validDomainID(itemID) {
		return metadataList[T]{}, ErrDomainInput
	}
	purpose := "catalog-owned-list-cursor-v1\x00" + projectionPair(kind, itemID)
	return readMetadataPage(ctx, auth, token, cursor, limit, purpose, func(ctx context.Context, tx *sql.Tx, afterID string, size int) (metadataPage[T], error) {
		// An absent owner or its missing/corrupt counter is never an empty page.
		if _, err := loadItem(ctx, tx, itemID); err != nil {
			return metadataPage[T]{}, err
		}
		if kind == "tags" {
			// ponytail: full legacy membership validation is O(n) per page; use
			// bounded counter joins if measured large-set read latency requires it.
			if err := validateItemTagAuthority(ctx, tx, itemID); err != nil {
				return metadataPage[T]{}, err
			}
		}
		rows, err := tx.QueryContext(ctx, query, itemID, afterID, size+1)
		if err != nil {
			return metadataPage[T]{}, err
		}
		defer rows.Close()
		page := metadataPage[T]{Data: make([]T, 0)}
		for rows.Next() {
			value, err := scan(rows)
			if err != nil {
				return metadataPage[T]{}, err
			}
			page.Data = append(page.Data, value)
		}
		if err := rows.Err(); err != nil {
			return metadataPage[T]{}, err
		}
		if len(page.Data) > size {
			page.Data = page.Data[:size]
			page.NextID = identity(page.Data[size-1])
		}
		return page, nil
	})
}
