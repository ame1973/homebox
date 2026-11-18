package httpapi

import "net/http"

func (a *authAPI) serveConnectivity(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if r.URL.Path != "/api/v1/system/connectivity" {
		return false
	}
	// Passive probes must not use requireAdmin: its activity write extends the
	// session's idle deadline. Unsafe methods still require the usual write gate.
	token, err := sessionToken(r)
	if err != nil {
		writeAuthError(w, err, requestID)
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		var ok bool
		token, _, ok = a.writeCredentials(w, r, requestID)
		if !ok {
			return true
		}
	}
	if err = a.auth.ProbeConnectivity(r.Context(), token); err != nil {
		writeAuthError(w, err, requestID)
		return true
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use the exact connectivity path without query parameters.", requestID)
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	if err = a.auth.ProbeConnectivity(r.Context(), token); err != nil {
		writeAuthError(w, err, requestID)
		return true
	}
	writeJSON(w, struct {
		Status string `json:"status"`
	}{Status: "ok"})
	return true
}
