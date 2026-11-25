package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A live diagnostic entry names original source proofs, not search text or a
// complete-coverage certificate. A retained counter can exist without a current
// projection, and a current projection does not prove historical provenance.
type syncSearchCoverageEntry struct {
	EntityType     string `json:"entity_type"`
	EntityID       string `json:"entity_id"`
	ItemID         string `json:"item_id"`
	ItemRevision   int64  `json:"item_revision"`
	UnitRevision   *int64 `json:"unit_revision"`
	SearchRevision *int64 `json:"search_revision"`
	Current        bool   `json:"current"`
}

type syncSearchCoveragePage struct {
	Data           []syncSearchCoverageEntry `json:"data"`
	NextCursor     *string                   `json:"next_cursor"`
	SourceRevision int64                     `json:"source_revision"`
}

// pageSyncSearchCoverage reads one live page, including validated lookahead.
// It does not authorize a backfill, certify an entire traversal, or enable sync.
func (a *Auth) pageSyncSearchCoverage(ctx context.Context, token, kind, cursor string, limit int) (syncSearchCoveragePage, error) {
	if ctx == nil || a == nil || a.store == nil || a.store.db == nil ||
		(kind != "item" && kind != "unit") || limit < 1 || limit > 20 || len(cursor) > 2048 {
		return syncSearchCoveragePage{}, ErrDomainInput
	}
	if err := ctx.Err(); err != nil {
		return syncSearchCoveragePage{}, err
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return syncSearchCoveragePage{}, err
	}
	defer tx.Rollback()
	session, err := a.loadSession(ctx, tx, token, a.now().UTC())
	if err != nil {
		return syncSearchCoveragePage{}, err
	}
	if err = requireCompleteSession(session); err != nil {
		return syncSearchCoveragePage{}, err
	}
	if err = finishDomainSession(ctx, a, tx, token, session); err != nil {
		return syncSearchCoveragePage{}, err
	}
	if _, err = verifySyncSearchSigner(ctx, tx, a.serverSecret); err != nil {
		return syncSearchCoveragePage{}, err
	}
	dataset, err := readSyncDataset(ctx, tx)
	if err != nil {
		return syncSearchCoveragePage{}, err
	}
	watermark, _, err := readSyncWatermark(ctx, tx, dataset.Floor)
	if err != nil {
		return syncSearchCoveragePage{}, err
	}
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM data_revision WHERE scope='catalog'").Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (revision < 0 || revision > maxSafeInteger) {
		return syncSearchCoveragePage{}, ErrDomainState
	}
	if err != nil {
		return syncSearchCoveragePage{}, err
	}
	// The continuation is signed, not encrypted, and is invalidated when these
	// observed scopes change. It is still not a frozen traversal or authority.
	purpose := fmt.Sprintf("sync-search-coverage-v1:%s:%s:%d:%d:%d:%d", kind, dataset.Instance, dataset.Version, dataset.Floor, watermark, revision)
	result, err := readMetadataPageTx(ctx, tx, a, token, cursor, limit, purpose,
		func(ctx context.Context, tx *sql.Tx, afterID string, limit int) (metadataPage[syncSearchCoverageEntry], error) {
			return readSyncSearchCoverage(ctx, tx, dataset, kind, afterID, limit)
		})
	if err != nil {
		return syncSearchCoveragePage{}, err
	}
	if _, err = checkDomainSession(ctx, a, tx, token, session, a.now().UTC()); err != nil {
		return syncSearchCoveragePage{}, err
	}
	if err = tx.Commit(); err != nil {
		return syncSearchCoveragePage{}, err
	}
	return syncSearchCoveragePage{Data: result.Data, NextCursor: result.NextCursor, SourceRevision: revision}, nil
}

func readSyncSearchCoverage(ctx context.Context, tx *sql.Tx, dataset syncDataset, kind, afterID string, limit int) (metadataPage[syncSearchCoverageEntry], error) {
	statement := "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM items"
	if kind == "unit" {
		statement = "SELECT CASE WHEN length(CAST(id AS BLOB))<=512 THEN id END FROM item_units"
	}
	var args []any
	if afterID != "" {
		statement += " WHERE id>?"
		args = append(args, afterID)
	}
	statement += " ORDER BY id LIMIT ?"
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return metadataPage[syncSearchCoverageEntry]{}, err
	}
	ids := make([]string, 0, limit+1)
	readErr := func() error {
		previous := afterID
		for rows.Next() {
			var id sql.NullString
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if !id.Valid || !validDomainID(id.String) || id.String <= previous {
				return ErrDomainState
			}
			ids = append(ids, id.String)
			previous = id.String
		}
		return nil
	}()
	if err = errors.Join(readErr, rows.Err(), rows.Close()); err != nil {
		return metadataPage[syncSearchCoverageEntry]{}, err
	}
	page := metadataPage[syncSearchCoverageEntry]{Data: make([]syncSearchCoverageEntry, 0, limit)}
	for _, id := range ids {
		entry, err := readSyncSearchCoverageEntry(ctx, tx, dataset, kind, id)
		if err != nil {
			return metadataPage[syncSearchCoverageEntry]{}, err
		}
		page.Data = append(page.Data, entry)
	}
	if len(page.Data) > limit {
		page.NextID = page.Data[limit-1].EntityID
		page.Data = page.Data[:limit]
	}
	return page, nil
}

func readSyncSearchCoverageEntry(ctx context.Context, tx *sql.Tx, dataset syncDataset, kind, id string) (syncSearchCoverageEntry, error) {
	entry := syncSearchCoverageEntry{EntityType: kind, EntityID: id, ItemID: id}
	if kind == "unit" {
		unit, err := loadUnit(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return syncSearchCoverageEntry{}, ErrDomainState
		}
		if err != nil {
			return syncSearchCoverageEntry{}, err
		}
		entry.ItemID, entry.UnitRevision = unit.ItemID, &unit.Revision
	}
	item, err := loadItem(ctx, tx, entry.ItemID)
	if errors.Is(err, ErrNotFound) {
		return syncSearchCoverageEntry{}, ErrDomainState
	}
	if err != nil {
		return syncSearchCoverageEntry{}, err
	}
	entry.ItemRevision = item.Revision
	identity := projectionPair(kind, id)
	counter, err := readSyncSearchCounter(ctx, tx, dataset, identity)
	if err != nil {
		return syncSearchCoverageEntry{}, err
	}
	if counter.Valid {
		entry.SearchRevision = &counter.Int64
	}
	current, present, err := readSyncSearchCurrent(ctx, tx, identity)
	if err != nil {
		return syncSearchCoverageEntry{}, err
	}
	if present {
		err = validateSearchProjectionSource(ctx, tx, identity, current.revision, []byte(current.payload))
	} else {
		_, err = readSearchProjectionText(ctx, tx, kind, id)
	}
	if errors.Is(err, ErrNotFound) {
		return syncSearchCoverageEntry{}, ErrDomainState
	}
	if err != nil {
		return syncSearchCoverageEntry{}, err
	}
	entry.Current = present
	return entry, nil
}
