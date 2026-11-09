package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"homebox/internal/store"
)

func assetRoute(path string) (route, id, allow string) {
	if path == "/api/v1/assets" {
		return "collection", "", http.MethodGet
	}
	if !strings.HasPrefix(path, "/api/v1/") {
		return "", "", ""
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) != 3 || parts[0] != "units" || parts[1] == "" {
		return "", "", ""
	}
	switch parts[2] {
	case "asset":
		return "asset", parts[1], "GET, PUT"
	case "usage", "costs", "disposal", "disposal-corrections":
		return parts[2], parts[1], "GET, POST"
	case "disposal-record":
		return "disposal-record", parts[1], http.MethodGet
	case "retire":
		return "retire", parts[1], http.MethodPost
	case "cost-summary":
		return "summary", parts[1], http.MethodGet
	default:
		return "", "", ""
	}
}

func (a *authAPI) serveAssets(w http.ResponseWriter, r *http.Request, requestID string) bool {
	route, id, allow := assetRoute(r.URL.Path)
	if route == "" {
		return false
	}
	if !a.requireAdmin(w, r, requestID) {
		return true
	}
	if !slices.Contains(strings.Split(allow, ", "), r.Method) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The method is not supported for this route.", requestID)
		return true
	}
	queryAllowed := r.Method == http.MethodGet && (route == "collection" || route == "usage" || route == "costs" || route == "disposal-corrections" || route == "summary")
	if r.URL.RawPath != "" || strings.ContainsAny(id, "\\\x00") || id == "." || id == ".." || id != "" && !boundedString(id, 1, 128) ||
		!queryAllowed && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_ASSET_URL", "Use an exact asset path and only documented read parameters.", requestID)
		return true
	}
	token, _ := sessionToken(r)
	if r.Method == http.MethodGet {
		if !requireNoBody(w, r, requestID) {
			return true
		}
		switch route {
		case "collection":
			cursor, size, filters, ok := assetListQuery(w, r, requestID)
			if !ok {
				return true
			}
			value, err := a.assetFinance.Page(r.Context(), token, cursor, size, filters)
			if errors.Is(err, store.ErrDomainInput) {
				assetQueryError(w, requestID)
			} else if err != nil {
				writeAssetError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		case "usage", "costs":
			cursor, size, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
			if !ok {
				return true
			}
			if route == "usage" {
				value, err := a.assetFinance.PageUsage(r.Context(), token, id, cursor, size)
				if err != nil {
					writeAssetError(w, err, requestID)
				} else {
					writeJSON(w, value)
				}
			} else {
				value, err := a.assetFinance.PageCosts(r.Context(), token, id, cursor, size)
				if err != nil {
					writeAssetError(w, err, requestID)
				} else {
					writeJSON(w, value)
				}
			}
		case "asset":
			value, err := a.assetFinance.Get(r.Context(), token, id)
			writeAssetResult(w, value, value.Revision, err, requestID)
		case "disposal":
			value, err := a.assetFinance.GetDisposal(r.Context(), token, id)
			writeAssetResult(w, value, value.Revision, err, requestID)
		case "disposal-record":
			value, err := a.assetFinance.GetDisposalRecord(r.Context(), token, id)
			writeAssetResult(w, value, value.Revision, err, requestID)
		case "disposal-corrections":
			cursor, size, ok := boundedMetadataQuery(w, r, requestID, 50, 16<<10)
			if !ok {
				return true
			}
			value, err := a.assetFinance.PageDisposalCorrections(r.Context(), token, id, cursor, size)
			if err != nil {
				writeAssetError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		case "summary":
			asOf, ok := assetSummaryQuery(w, r, requestID)
			if !ok {
				return true
			}
			value, err := a.assetFinance.CostSummary(r.Context(), token, id, asOf)
			if errors.Is(err, store.ErrDomainInput) {
				assetQueryError(w, requestID)
			} else if err != nil {
				writeAssetError(w, err, requestID)
			} else {
				writeJSON(w, value)
			}
		}
		return true
	}
	revision, _, ok := resourceRevision(w, r, requestID, true)
	if !ok {
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	switch route {
	case "asset":
		input, ok := readAssetProfile(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.assetFinance.PutProfile(r.Context(), token, key, id, revision, input)
		writeAssetResult(w, value, value.Revision, err, requestID)
	case "usage":
		input, ok := readAssetUsage(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.assetFinance.RecordUsage(r.Context(), token, key, id, revision, input)
		writeAssetResult(w, value, value.Revision, err, requestID)
	case "costs":
		input, ok := readAssetCost(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.assetFinance.RecordCost(r.Context(), token, key, id, revision, input)
		writeAssetResult(w, value, value.Revision, err, requestID)
	case "retire":
		input, ok := readAssetRetirement(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.assetFinance.Retire(r.Context(), token, key, id, revision, input)
		writeAssetResult(w, value, value.Revision, err, requestID)
	case "disposal":
		input, ok := readAssetDisposal(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.assetFinance.Dispose(r.Context(), token, key, id, revision, input)
		writeAssetResult(w, value, value.Revision, err, requestID)
	case "disposal-corrections":
		input, ok := readDisposalCorrection(w, r, requestID)
		if !ok {
			return true
		}
		value, err := a.assetFinance.CorrectDisposal(r.Context(), token, key, id, revision, input)
		writeAssetResult(w, value, value.Revision, err, requestID)
	}
	return true
}

func writeAssetResult(w http.ResponseWriter, value any, revision int64, err error, requestID string) {
	if err != nil {
		writeAssetError(w, err, requestID)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(revision, 10)))
	writeJSON(w, value)
}

func writeAssetError(w http.ResponseWriter, err error, requestID string) {
	status, code, message := http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Asset data is temporarily unavailable. Review the source and retry explicitly later."
	switch {
	case errors.Is(err, store.ErrAuthentication), errors.Is(err, store.ErrPasswordChangeRequired), errors.Is(err, store.ErrIdempotencyConflict):
		writeAuthError(w, err, requestID)
		return
	case errors.Is(err, store.ErrDomainInput):
		assetValidation(w, requestID)
		return
	case errors.Is(err, store.ErrNotFound):
		status, code, message = http.StatusNotFound, "NOT_FOUND", "The Asset or referenced resource does not exist."
	case errors.Is(err, store.ErrInvalidCursor):
		status, code, message = http.StatusBadRequest, "INVALID_CURSOR", "Refresh the collection; its continuation is invalid, expired or differently scoped."
	case errors.Is(err, store.ErrPreconditionRequired):
		status, code, message = http.StatusConflict, "PRECONDITION_REQUIRED", "Supply the original reviewed Disposal, Unit or Asset proof required by this operation."
	case errors.Is(err, store.ErrRevisionConflict):
		status, code, message = http.StatusConflict, "REVISION_CONFLICT", "The original reviewed Disposal, Unit, Item, Asset or profile presence changed. Refresh and review before a new command."
	case errors.Is(err, store.ErrIdempotencyExpired):
		status, code, message = http.StatusConflict, "IDEMPOTENCY_EXPIRED", "The original acknowledgement expired. Review current source before using a new key."
	case errors.Is(err, store.ErrAssetCurrency):
		status, code, message = http.StatusConflict, "ASSET_CURRENCY_CONFLICT", "New asset money requires a supported finite-minor-unit currency compatible with its actual financial source."
	case errors.Is(err, store.ErrAssetOverflow):
		status, code, message = http.StatusConflict, "ASSET_TOTAL_OVERFLOW", "Complete proposed asset totals exceed the exact safe financial range. Review the source without automatic conversion or replay."
	case errors.Is(err, store.ErrAssetHistory):
		status, code, message = http.StatusConflict, "ASSET_HISTORY_LOCKED", "The recorded lifecycle or financial decision cannot be repeated or overwritten. Review its retained history."
	case errors.Is(err, store.ErrUnitAssetLifecycle):
		status, code, message = http.StatusConflict, "ASSET_LIFECYCLE_REQUIRED", "Retirement changes require the separately reviewed financial and physical lifecycle operation."
	case errors.Is(err, store.ErrUnitDisposed):
		status, code, message = http.StatusConflict, "UNIT_DISPOSED", "The disposed Unit cannot be revived or retired by this command."
	case errors.Is(err, store.ErrUnitTracking):
		status, code, message = http.StatusConflict, "TRACKING_MODE_CONFLICT", "An asset profile requires an actual individually tracked Unit."
	}
	writeError(w, status, code, message, requestID)
}
