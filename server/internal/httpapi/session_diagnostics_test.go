package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Session diagnostics are test-only metadata, never a credential or token hash.
type sessionFailureMetadata struct {
	created, seen, expires            string
	revoked, epochMatches, mustChange bool
}

func readSessionFailureMetadata(ctx context.Context, path string, cookie *http.Cookie) (sessionFailureMetadata, error) {
	var result sessionFailureMetadata
	if cookie == nil || cookie.Value == "" {
		return result, fmt.Errorf("missing test session")
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout%285000%29"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return result, err
	}
	defer db.Close()
	hash := sha256.Sum256([]byte(cookie.Value))
	err = db.QueryRowContext(ctx, `SELECT s.created_at,s.last_seen_at,s.expires_at,s.revoked_at IS NOT NULL,
 s.dataset_epoch=d.dataset_epoch,a.must_change_password FROM sessions s JOIN admin_accounts a ON a.id=s.admin_id
 JOIN sync_state d ON d.singleton=1 WHERE s.token_hash=?`, hex.EncodeToString(hash[:])).
		Scan(&result.created, &result.seen, &result.expires, &result.revoked, &result.epochMatches, &result.mustChange)
	return result, err
}

func logSessionFailure(t *testing.T, path string, cookie *http.Cookie, started, ended time.Time) {
	t.Helper()
	metadata, err := readSessionFailureMetadata(t.Context(), path, cookie)
	if err != nil {
		t.Log("cannot inspect test session metadata:", err)
		return
	}
	t.Logf("session timing: request_started=%s request_ended=%s observed=%s created=%s seen=%s expires=%s revoked=%t epoch_matches=%t must_change=%t",
		started.UTC().Format(time.RFC3339Nano), ended.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano),
		metadata.created, metadata.seen, metadata.expires, metadata.revoked, metadata.epochMatches, metadata.mustChange)
}

func TestSessionFailureDiagnosticsReadOnlyNonSecretMetadata(t *testing.T) {
	var path string
	_, _, cookie, _ := domainHTTPFixture(t, &path)
	metadata, err := readSessionFailureMetadata(t.Context(), path, cookie)
	if err != nil || metadata.created == "" || metadata.seen == "" || metadata.expires == "" || metadata.revoked || !metadata.epochMatches || metadata.mustChange {
		t.Fatal("private fixture session metadata was unavailable or incorrect", metadata, err)
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	text := fmt.Sprintf("%+v", metadata)
	if strings.Contains(text, cookie.Value) || strings.Contains(text, hex.EncodeToString(hash[:])) {
		t.Fatal("session diagnostic included a credential or its digest")
	}
	if _, err = readSessionFailureMetadata(t.Context(), path, nil); err == nil {
		t.Fatal("missing test session unexpectedly produced metadata")
	}
}
