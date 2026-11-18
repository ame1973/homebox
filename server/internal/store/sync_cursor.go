package store

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrSyncResetRequired   = errors.New("sync cursor belongs to a different dataset or projection")
	ErrSyncSnapshotExpired = errors.New("sync snapshot has expired")
)

const (
	syncSnapshotCursorPurpose = "sync-snapshot-cursor-v1"
	syncDeltaCursorPurpose    = "sync-delta-cursor-v1"
	syncSnapshotLifetime      = 15 * time.Minute
	maxSyncCursorBytes        = 4096
)

// These authenticated continuations do not authorize a read. The caller must
// still validate the active completed session and current database state.
type syncCursorScope struct {
	AdminID           string `json:"admin"`
	ServerInstanceID  string `json:"instance"`
	DatasetEpoch      string `json:"epoch"`
	ProjectionVersion int64  `json:"projection_version"`
}

type syncSnapshotCursor struct {
	Version       int             `json:"v"`
	Scope         syncCursorScope `json:"scope"`
	SnapshotID    string          `json:"snapshot_id"`
	NextOrdinal   int64           `json:"next_ordinal"`
	HighWatermark int64           `json:"high_watermark"`
	CreatedAt     string          `json:"created_at"`
	ExpiresAt     string          `json:"expires_at"`
}

type syncDeltaCursor struct {
	Version int             `json:"v"`
	Scope   syncCursorScope `json:"scope"`
	After   int64           `json:"after"`
}

func validSyncCursorScope(scope syncCursorScope) bool {
	return validDomainID(scope.AdminID) && validMediaInstance(scope.ServerInstanceID) &&
		authString(scope.DatasetEpoch, 1, 128) && scope.ProjectionVersion >= 1 && scope.ProjectionVersion <= maxSafeInteger
}

func syncSnapshotTimes(value syncSnapshotCursor) (time.Time, time.Time, bool) {
	created, err := time.Parse(authTimestamp, value.CreatedAt)
	if err != nil || created.Year() < 1 || created.Year() > 9999 || stamp(created) != value.CreatedAt {
		return time.Time{}, time.Time{}, false
	}
	expires, err := time.Parse(authTimestamp, value.ExpiresAt)
	if err != nil || expires.Year() < 1 || expires.Year() > 9999 || stamp(expires) != value.ExpiresAt || !expires.Equal(created.Add(syncSnapshotLifetime)) {
		return time.Time{}, time.Time{}, false
	}
	return created, expires, true
}

func validSyncSnapshotCursor(value syncSnapshotCursor) bool {
	_, _, timesValid := syncSnapshotTimes(value)
	return value.Version == 1 && validSyncCursorScope(value.Scope) && validDomainID(value.SnapshotID) &&
		value.NextOrdinal >= 0 && value.NextOrdinal <= maxSafeInteger && value.HighWatermark >= 0 && value.HighWatermark <= maxSafeInteger && timesValid
}

func validSyncDeltaCursor(value syncDeltaCursor) bool {
	return value.Version == 1 && validSyncCursorScope(value.Scope) && value.After >= 0 && value.After <= maxSafeInteger
}

func encodeSyncSnapshotCursor(a *Auth, value syncSnapshotCursor) (string, error) {
	if !validSyncSnapshotCursor(value) || value.Scope.ProjectionVersion != 1 {
		return "", ErrDomainState
	}
	return signSyncCursor(a, syncSnapshotCursorPurpose, value)
}

func encodeSyncDeltaCursor(a *Auth, value syncDeltaCursor) (string, error) {
	if !validSyncDeltaCursor(value) || value.Scope.ProjectionVersion != 1 {
		return "", ErrDomainState
	}
	return signSyncCursor(a, syncDeltaCursorPurpose, value)
}

func signSyncCursor(a *Auth, purpose string, value any) (string, error) {
	if a == nil || len(a.serverSecret) != 32 {
		return "", ErrDomainState
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(a.authMAC("", purpose, body))
	if len(encoded) > maxSyncCursorBytes {
		return "", ErrDomainState
	}
	return encoded, nil
}

func decodeSignedSyncCursor(a *Auth, purpose, token string, destination any) error {
	if a == nil || len(a.serverSecret) != 32 {
		return ErrDomainState
	}
	if len(token) > maxSyncCursorBytes {
		return ErrInvalidCursor
	}
	encodedBody, encodedSignature, found := strings.Cut(token, ".")
	if !found || encodedBody == "" || len(encodedSignature) != 43 {
		return ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.Strict().DecodeString(encodedBody)
	if err != nil || base64.RawURLEncoding.EncodeToString(body) != encodedBody {
		return ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(encodedSignature)
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != encodedSignature ||
		subtle.ConstantTimeCompare(signature, a.authMAC("", purpose, body)) != 1 {
		return ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if !json.Valid(body) || decoder.Decode(destination) != nil {
		return ErrInvalidCursor
	}
	canonical, err := json.Marshal(destination)
	if err != nil || !bytes.Equal(canonical, body) {
		return ErrInvalidCursor
	}
	return nil
}

func validateSyncCursorScope(actual, expected syncCursorScope) error {
	if !validSyncCursorScope(expected) || expected.ProjectionVersion != 1 {
		return ErrDomainState
	}
	if actual.AdminID != expected.AdminID {
		return ErrInvalidCursor
	}
	if actual.ServerInstanceID != expected.ServerInstanceID || actual.DatasetEpoch != expected.DatasetEpoch || actual.ProjectionVersion != expected.ProjectionVersion {
		return ErrSyncResetRequired
	}
	return nil
}

func decodeSyncSnapshotCursor(a *Auth, token string, expected syncCursorScope, now time.Time) (syncSnapshotCursor, error) {
	var value syncSnapshotCursor
	if !validSyncCursorScope(expected) || expected.ProjectionVersion != 1 {
		return syncSnapshotCursor{}, ErrDomainState
	}
	if err := decodeSignedSyncCursor(a, syncSnapshotCursorPurpose, token, &value); err != nil {
		return syncSnapshotCursor{}, err
	}
	if !validSyncSnapshotCursor(value) {
		return syncSnapshotCursor{}, ErrInvalidCursor
	}
	if err := validateSyncCursorScope(value.Scope, expected); err != nil {
		return syncSnapshotCursor{}, err
	}
	created, expires, _ := syncSnapshotTimes(value)
	if now.Before(created) {
		return syncSnapshotCursor{}, ErrInvalidCursor
	}
	if !now.Before(expires) {
		return syncSnapshotCursor{}, ErrSyncSnapshotExpired
	}
	return value, nil
}

// A delta token deliberately has no wall-clock expiry. Its sequence must be
// compared with the retained floor and durable watermark in the same authorized
// SQLite transaction that reads the frozen changes.
func checkSyncDeltaPosition(after, floor, watermark int64) error {
	if floor < 0 || watermark < floor || watermark > maxSafeInteger {
		return ErrDomainState
	}
	if after < 0 || after > maxSafeInteger {
		return ErrInvalidCursor
	}
	if after < floor || after > watermark {
		return ErrSyncResetRequired
	}
	return nil
}

func decodeSyncDeltaCursor(a *Auth, token string, expected syncCursorScope) (syncDeltaCursor, error) {
	var value syncDeltaCursor
	if !validSyncCursorScope(expected) || expected.ProjectionVersion != 1 {
		return syncDeltaCursor{}, ErrDomainState
	}
	if err := decodeSignedSyncCursor(a, syncDeltaCursorPurpose, token, &value); err != nil {
		return syncDeltaCursor{}, err
	}
	if !validSyncDeltaCursor(value) {
		return syncDeltaCursor{}, ErrInvalidCursor
	}
	if err := validateSyncCursorScope(value.Scope, expected); err != nil {
		return syncDeltaCursor{}, err
	}
	return value, nil
}
