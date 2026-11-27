package httpapi

import (
	"errors"
	"net/http"

	"homebox/internal/store"
)

// The database filesystem observation is not a reservation or a Media or
// backup inventory; unmeasured scopes remain explicitly null.
type systemStorage struct {
	DatabaseBytes                int64  `json:"database_bytes"`
	MediaBytes                   *int64 `json:"media_bytes"`
	FreeBytes                    int64  `json:"free_bytes"`
	BackupBytes                  *int64 `json:"backup_bytes"`
	DatabaseFilesystemTotalBytes int64  `json:"database_filesystem_total_bytes"`
	MeasuredAt                   string `json:"measured_at"`
}

func (a *authAPI) serveStorage(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if r.URL.Path != "/api/v1/system/storage" {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use the exact storage path without query parameters.", requestID)
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	token, _ := sessionToken(r)
	status, err := a.auth.ReadDatabaseStorage(r.Context(), token)
	if err != nil {
		if errors.Is(err, store.ErrAuthentication) || errors.Is(err, store.ErrPasswordChangeRequired) {
			writeAuthError(w, err, requestID)
		} else {
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "The storage observation is temporarily unavailable. Retry explicitly later.", requestID)
		}
		return true
	}
	writeJSON(w, systemStorage{
		DatabaseBytes: status.DatabaseBytes, FreeBytes: status.AvailableBytes,
		DatabaseFilesystemTotalBytes: status.TotalBytes, MeasuredAt: status.MeasuredAt,
	})
	return true
}
