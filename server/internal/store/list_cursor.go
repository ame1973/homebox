package store

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrInvalidCursor = errors.New("invalid, expired, or differently scoped list cursor")

const (
	metadataCursorLifetime = 15 * time.Minute
	locationCursorPurpose  = "locations-tree-cursor-v1"
	categoryCursorPurpose  = "categories-list-cursor-v1"
	tagCursorPurpose       = "tags-list-cursor-v1"
)

// Internal pages carry unsigned continuation data, never a public cursor.
type metadataPage[T any] struct {
	Data   []T    `json:"data"`
	NextID string `json:"-"`
}

// Live metadata lists are not materialized offline snapshots. A continuation
// never authorizes a read or extends the independently validated session.
type metadataList[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

type LocationList = metadataList[Location]
type CategoryList = metadataList[Category]
type TagList = metadataList[Tag]

type metadataCursor struct {
	AdminID string `json:"admin"`
	Epoch   string `json:"epoch"`
	AfterID string `json:"after"`
	Limit   int    `json:"limit"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
}

func (l *Locations) Page(ctx context.Context, token, cursor string, limit int) (LocationList, error) {
	return readMetadataPage(ctx, l.auth, token, cursor, limit, locationCursorPurpose, listLocations)
}

func (taxonomy *Taxonomy) PageCategories(ctx context.Context, token, cursor string, limit int) (CategoryList, error) {
	return readMetadataPage(ctx, taxonomy.auth, token, cursor, limit, categoryCursorPurpose, listCategories)
}

func (taxonomy *Taxonomy) PageTags(ctx context.Context, token, cursor string, limit int) (TagList, error) {
	return readMetadataPage(ctx, taxonomy.auth, token, cursor, limit, tagCursorPurpose, listTags)
}

func readMetadataPage[T any](ctx context.Context, a *Auth, token, cursor string, limit int, purpose string,
	list func(context.Context, *sql.Tx, string, int) (metadataPage[T], error),
) (metadataList[T], error) {
	if limit < 1 || limit > 100 || len(cursor) > 2048 {
		return metadataList[T]{}, ErrDomainInput
	}
	return readDomain(ctx, a, token, func(tx *sql.Tx) (metadataList[T], error) {
		return readMetadataPageTx(ctx, tx, a, token, cursor, limit, purpose, list)
	})
}

// Reuse the cursor/read boundary inside an existing authorized transaction. The
// caller may resolve filter settings there; never nest a public readDomain call.
func readMetadataPageTx[T any](ctx context.Context, tx *sql.Tx, a *Auth, token, cursor string, limit int, purpose string,
	list func(context.Context, *sql.Tx, string, int) (metadataPage[T], error),
) (metadataList[T], error) {
	return readMetadataIdentityPageTx(ctx, tx, a, token, cursor, limit, purpose, 2048, validDomainID, list)
}

// Composite Stock positions need their original component bounds and a larger
// token budget. Existing single-ID purposes retain the exact old wire spelling.
func readMetadataIdentityPageTx[T any](ctx context.Context, tx *sql.Tx, a *Auth, token, cursor string, limit int, purpose string,
	maxCursorBytes int, validIdentity func(string) bool, list func(context.Context, *sql.Tx, string, int) (metadataPage[T], error),
) (metadataList[T], error) {
	if limit < 1 || limit > 100 || len(cursor) > maxCursorBytes {
		return metadataList[T]{}, ErrDomainInput
	}
	now := a.now().UTC()
	session, err := a.loadSession(ctx, tx, token, now)
	if err != nil {
		return metadataList[T]{}, err
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, "SELECT dataset_epoch FROM sync_state WHERE singleton=1").Scan(&epoch); err != nil {
		return metadataList[T]{}, err
	}
	afterID := ""
	if cursor != "" {
		value, err := decodeMetadataIdentityCursor(a, purpose, cursor, session.Admin.ID, epoch, limit, now, maxCursorBytes, validIdentity)
		if err != nil {
			return metadataList[T]{}, err
		}
		afterID = value.AfterID
	}
	page, err := list(ctx, tx, afterID, limit)
	if err != nil {
		return metadataList[T]{}, err
	}
	result := metadataList[T]{Data: page.Data}
	if page.NextID != "" {
		if !validIdentity(page.NextID) {
			return metadataList[T]{}, ErrDomainState
		}
		value := metadataCursor{AdminID: session.Admin.ID, Epoch: epoch, AfterID: page.NextID,
			Limit: limit, Issued: now.Unix(), Expires: now.Add(metadataCursorLifetime).Unix()}
		next, err := encodeMetadataCursor(a, purpose, value)
		if err != nil {
			return metadataList[T]{}, err
		}
		if len(next) > maxCursorBytes {
			return metadataList[T]{}, ErrDomainState
		}
		result.NextCursor = &next
	}
	return result, nil
}

func encodeMetadataCursor(a *Auth, purpose string, value metadataCursor) (string, error) {
	if len(a.serverSecret) != 32 {
		return "", ErrDomainState
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// Empty credential input is intentional: this is a principal/epoch-scoped
	// read continuation, not a session-bound credential or reauth capability.
	signature := a.authMAC("", purpose, body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func decodeMetadataCursor(a *Auth, purpose, token, adminID, epoch string, limit int, now time.Time) (metadataCursor, error) {
	return decodeMetadataIdentityCursor(a, purpose, token, adminID, epoch, limit, now, 2048, validDomainID)
}

func decodeMetadataIdentityCursor(a *Auth, purpose, token, adminID, epoch string, limit int, now time.Time, maxCursorBytes int, validIdentity func(string) bool) (metadataCursor, error) {
	var zero metadataCursor
	if len(a.serverSecret) != 32 {
		return zero, ErrDomainState
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) != 43 || len(token) > maxCursorBytes {
		return zero, ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(body) != parts[0] {
		return zero, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[1] ||
		subtle.ConstantTimeCompare(signature, a.authMAC("", purpose, body)) != 1 {
		return zero, ErrInvalidCursor
	}
	var value metadataCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if !json.Valid(body) || decoder.Decode(&value) != nil || value.AdminID != adminID || value.Epoch != epoch || value.Limit != limit ||
		!validIdentity(value.AfterID) || value.Issued < 0 || value.Expires <= value.Issued ||
		value.Expires-value.Issued != int64(metadataCursorLifetime/time.Second) || now.Unix() < value.Issued || now.Unix() >= value.Expires {
		return zero, ErrInvalidCursor
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, body) {
		return zero, ErrInvalidCursor
	}
	return value, nil
}
