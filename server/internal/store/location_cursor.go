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

const locationCursorLifetime = 15 * time.Minute

// LocationList contains only public wire data; unsigned continuation IDs remain
// internal. This ordinary list is not a materialized offline snapshot.
type LocationList struct {
	Data       []Location `json:"data"`
	NextCursor *string    `json:"next_cursor"`
}

type locationCursor struct {
	AdminID string `json:"admin"`
	Epoch   string `json:"epoch"`
	AfterID string `json:"after"`
	Limit   int    `json:"limit"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
}

// Page authenticates every read and signs continuation state with private server
// key material. A cursor grants no access on its own and never extends a session.
func (l *Locations) Page(ctx context.Context, token, cursor string, limit int) (LocationList, error) {
	if limit < 1 || limit > 100 || len(cursor) > 2048 {
		return LocationList{}, ErrDomainInput
	}
	return readDomain(ctx, l.auth, token, func(tx *sql.Tx) (LocationList, error) {
		now := l.auth.now().UTC()
		session, err := l.auth.loadSession(ctx, tx, token, now)
		if err != nil {
			return LocationList{}, err
		}
		var epoch string
		if err = tx.QueryRowContext(ctx, "SELECT dataset_epoch FROM sync_state WHERE singleton=1").Scan(&epoch); err != nil {
			return LocationList{}, err
		}
		afterID := ""
		if cursor != "" {
			value, err := l.decodeLocationCursor(cursor, session.Admin.ID, epoch, limit, now)
			if err != nil {
				return LocationList{}, err
			}
			afterID = value.AfterID
		}
		page, err := listLocations(ctx, tx, afterID, limit)
		if err != nil {
			return LocationList{}, err
		}
		result := LocationList{Data: page.Data}
		if page.NextID != "" {
			value := locationCursor{AdminID: session.Admin.ID, Epoch: epoch, AfterID: page.NextID,
				Limit: limit, Issued: now.Unix(), Expires: now.Add(locationCursorLifetime).Unix()}
			next, err := l.encodeLocationCursor(value)
			if err != nil {
				return LocationList{}, err
			}
			result.NextCursor = &next
		}
		return result, nil
	})
}

func (l *Locations) encodeLocationCursor(value locationCursor) (string, error) {
	if len(l.auth.serverSecret) != 32 {
		return "", ErrDomainState
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// Empty credential input is intentional: this is a principal/epoch-scoped
	// read continuation, not a session-bound credential or reauth capability.
	signature := l.auth.authMAC("", "locations-tree-cursor-v1", body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (l *Locations) decodeLocationCursor(token, adminID, epoch string, limit int, now time.Time) (locationCursor, error) {
	var zero locationCursor
	if len(l.auth.serverSecret) != 32 {
		return zero, ErrDomainState
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) != 43 || len(token) > 2048 {
		return zero, ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(body) != parts[0] {
		return zero, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[1] ||
		subtle.ConstantTimeCompare(signature, l.auth.authMAC("", "locations-tree-cursor-v1", body)) != 1 {
		return zero, ErrInvalidCursor
	}
	var value locationCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if !json.Valid(body) || decoder.Decode(&value) != nil || value.AdminID != adminID || value.Epoch != epoch || value.Limit != limit ||
		!validDomainID(value.AfterID) || value.Issued < 0 || value.Expires <= value.Issued ||
		value.Expires-value.Issued != int64(locationCursorLifetime/time.Second) || now.Unix() < value.Issued || now.Unix() >= value.Expires {
		return zero, ErrInvalidCursor
	}
	return value, nil
}
