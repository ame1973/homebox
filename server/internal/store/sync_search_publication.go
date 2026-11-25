package store

import (
	"crypto/subtle"
	"database/sql"
	"errors"
)

// A new proof is valid only for the exact source-derived bytes published in
// this transaction. It does not certify older retained search changes.
func (m *mutationTx) stageSearchProof(identity string, revision, seq int64, payload []byte) error {
	if m.auth == nil {
		return ErrDomainState
	}
	dataset, err := readSyncDataset(m.ctx, m.tx)
	if err != nil {
		return err
	}
	if dataset != m.dataset {
		return ErrDomainState
	}
	if err = validateSearchProjectionSource(m.ctx, m.tx, identity, revision, payload); err != nil {
		return err
	}
	pinned, err := verifySyncSearchSigner(m.ctx, m.tx, m.auth.serverSecret)
	if err != nil {
		return err
	}
	if pinned != m.searchSignerPinned {
		return ErrAuthState
	}
	receiptPinned, err := verifyKeyedReceiptSecret(m.ctx, m.tx, m.auth.serverSecret)
	if err != nil {
		return err
	}
	if receiptPinned != m.receiptPinned {
		return ErrAuthState
	}
	if err = pinSyncSearchSigner(m.ctx, m.tx, m.auth.serverSecret); err != nil {
		return err
	}
	m.searchSignerPinned = true
	receiptPinned, err = verifyKeyedReceiptSecret(m.ctx, m.tx, m.auth.serverSecret)
	if err != nil {
		return err
	}
	if receiptPinned != m.receiptPinned {
		return ErrAuthState
	}
	proof, err := m.auth.syncSearchProof(m.ctx, dataset.Instance, dataset.Epoch, identity, seq, revision, payload)
	if err != nil {
		return err
	}
	if err = requireDomainRow(m.tx.ExecContext(m.ctx, "INSERT INTO sync_search_proofs(seq,proof) VALUES(?,?)", seq, proof[:])); err != nil {
		return err
	}
	frozen := string(payload)
	m.searchProofChecks = append(m.searchProofChecks, func() error {
		return m.verifyPublishedSearchProof(dataset, identity, revision, seq, frozen, proof, receiptPinned)
	})
	return nil
}

// Recheck after audit, session activity and optional final callbacks. Exact
// current/frozen bytes and reviewed source must still match the signed input.
func (m *mutationTx) verifyPublishedSearchProof(dataset syncDataset, identity string, revision, seq int64, payload string, proof [32]byte, receiptPinned bool) error {
	pinned, err := verifySyncSearchSigner(m.ctx, m.tx, m.auth.serverSecret)
	if err != nil {
		return err
	}
	if !pinned {
		return ErrAuthState
	}
	currentReceiptPinned, err := verifyKeyedReceiptSecret(m.ctx, m.tx, m.auth.serverSecret)
	if err != nil {
		return err
	}
	if currentReceiptPinned != receiptPinned {
		return ErrAuthState
	}
	currentDataset, err := readSyncDataset(m.ctx, m.tx)
	if err != nil {
		return err
	}
	if currentDataset != dataset {
		return ErrDomainState
	}
	var kind, id, operation, frozen, created, current, updated sql.NullString
	var frozenRevision, currentRevision, version, counter int64
	var storedProof []byte
	err = m.tx.QueryRowContext(m.ctx, `SELECT
 CASE WHEN length(CAST(c.entity_type AS BLOB))<=32 THEN c.entity_type END,
 CASE WHEN length(CAST(c.entity_id AS BLOB))<=? THEN c.entity_id END,
 CASE WHEN length(CAST(c.operation AS BLOB))<=16 THEN c.operation END,
 c.entity_revision,CASE WHEN length(CAST(c.projection_json AS BLOB))=? THEN c.projection_json END,
 CASE WHEN length(CAST(c.created_at AS BLOB))<=35 THEN c.created_at END,
 CASE WHEN typeof(p.proof)='blob' AND length(p.proof)=32 THEN p.proof END,
 v.entity_revision,v.projection_version,CASE WHEN length(CAST(v.payload_json AS BLOB))=? THEN v.payload_json END,
 CASE WHEN length(CAST(v.updated_at AS BLOB))<=35 THEN v.updated_at END,r.revision
 FROM sync_changes c JOIN sync_search_proofs p ON p.seq=c.seq
 JOIN current_projections v ON v.entity_type=c.entity_type AND v.entity_id=c.entity_id
 JOIN projection_revisions r ON r.entity_type=v.entity_type AND r.entity_id=v.entity_id
 WHERE c.seq=?`, maxSyncIdentityBytes, len(payload), len(payload), seq).
		Scan(&kind, &id, &operation, &frozenRevision, &frozen, &created, &storedProof,
			&currentRevision, &version, &current, &updated, &counter)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDomainState
	}
	if err != nil {
		return err
	}
	if !kind.Valid || kind.String != "search_doc" || !id.Valid || id.String != identity || !operation.Valid || operation.String != "upsert" ||
		frozenRevision != revision || currentRevision != revision || counter != revision || version != 1 ||
		!frozen.Valid || frozen.String != payload || !current.Valid || current.String != payload ||
		!created.Valid || created.String != stamp(m.now) || !updated.Valid || updated.String != stamp(m.now) ||
		subtle.ConstantTimeCompare(storedProof, proof[:]) != 1 {
		return ErrDomainState
	}
	return validateSearchProjectionSource(m.ctx, m.tx, identity, revision, []byte(payload))
}
