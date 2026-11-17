package httpapi

import "net/http"

func (a *authAPI) serveSyncStatus(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if r.URL.Path != "/api/v1/sync/status" {
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
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use the exact status path without query parameters.", requestID)
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	token, _ := sessionToken(r)
	status, err := a.auth.ReadSyncStatus(r.Context(), token)
	if err != nil {
		writeAuthError(w, err, requestID)
	} else {
		writeJSON(w, status)
	}
	return true
}
