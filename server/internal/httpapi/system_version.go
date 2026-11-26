package httpapi

import (
	"net/http"
	"strings"
)

// The observation is not a write authorization, deployment-integrity proof,
// model inventory or evidence that an offline replica is ready.
type systemVersion struct {
	APIVersion                   string   `json:"api_version"`
	ProjectionVersion            int64    `json:"projection_version"`
	OfflineProjectionVersion     int64    `json:"offline_projection_version"`
	DatasetEpoch                 string   `json:"dataset_epoch"`
	ServerInstanceID             string   `json:"server_instance_id"`
	WebBuildID                   string   `json:"web_build_id"`
	WebVersion                   string   `json:"web_version"`
	SchemaVersion                int      `json:"schema_version"`
	MinSupportedClient           *string  `json:"min_supported_client"`
	MinWebBuildID                *string  `json:"min_web_build_id"`
	ModelIndexVersions           []string `json:"model_index_versions"`
	SupportedWriteClientVersions []string `json:"supported_write_client_versions"`
}

func (a *authAPI) serveVersion(w http.ResponseWriter, r *http.Request, requestID string) bool {
	if r.URL.Path != "/api/v1/system/version" {
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
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Use the exact version path without query parameters.", requestID)
		return true
	}
	if !requireNoBody(w, r, requestID) {
		return true
	}
	build, err := readWebBuild(a.assets)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "The frontend build metadata is unavailable. Retry explicitly later.", requestID)
		return true
	}
	token, _ := sessionToken(r)
	state, err := a.auth.ReadVersionState(r.Context(), token)
	if err != nil {
		writeAuthError(w, err, requestID)
		return true
	}
	writeJSON(w, systemVersion{
		APIVersion: "1", ProjectionVersion: state.ProjectionVersion, OfflineProjectionVersion: state.ProjectionVersion,
		DatasetEpoch: state.DatasetEpoch, ServerInstanceID: state.ServerInstanceID, SchemaVersion: state.SchemaVersion,
		WebBuildID: strings.TrimSuffix(strings.TrimPrefix(build.worker, "/pwa-worker-"), ".js"), WebVersion: build.version,
		SupportedWriteClientVersions: supportedWriteClientVersions(),
	})
	return true
}
