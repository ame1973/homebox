package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const authBodyLimit = 16 << 10

// Authentication inputs are flat, exact-name string objects. In particular,
// encoding/json's case-insensitive struct matching and last-duplicate-wins behavior
// are not appropriate at this credential boundary.
func readAuthJSON(w http.ResponseWriter, r *http.Request, requestID string, fields ...string) (map[string]string, bool) {
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Send an unencoded application/json request.", requestID)
		return nil, false
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Send application/json with UTF-8 encoding.", requestID)
		return nil, false
	}
	body, ok := readAuthBody(w, r, requestID)
	if !ok {
		return nil, false
	}
	if !utf8.Valid(body) || !json.Valid(body) || !validJSONSurrogates(body) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "The request must contain one valid UTF-8 JSON object.", requestID)
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, _ := decoder.Token()
	if first != json.Delim('{') {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "The request must be an object with the required string fields.", requestID)
		return nil, false
	}
	values := make(map[string]string, len(fields))
	for decoder.More() {
		name, _ := decoder.Token()
		key := name.(string)
		if _, duplicate := values[key]; duplicate {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", "Duplicate JSON fields are not accepted.", requestID)
			return nil, false
		}
		if !slices.Contains(fields, key) {
			writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "The request contains an unknown field.", requestID)
			return nil, false
		}
		var raw json.RawMessage
		_ = decoder.Decode(&raw) // json.Valid above guarantees token syntax.
		if len(raw) == 0 || raw[0] != '"' {
			writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Every authentication field must be a string.", requestID)
			return nil, false
		}
		var value string
		_ = json.Unmarshal(raw, &value)
		values[key] = value
	}
	if len(values) != len(fields) {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "A required authentication field is missing.", requestID)
		return nil, false
	}
	return values, true
}

func readAuthBody(w http.ResponseWriter, r *http.Request, requestID string) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, authBodyLimit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "The authentication body exceeds 16 KiB.", requestID)
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_BODY", "The request body could not be read.", requestID)
		}
		return nil, false
	}
	return body, true
}

// encoding/json replaces lone escaped surrogates with U+FFFD. Reject them rather
// than silently changing a submitted credential. Call only after json.Valid.
func validJSONSurrogates(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if body[i] != 'u' {
			continue
		}
		unit, _ := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		i += 4
		if unit < 0xD800 || unit > 0xDFFF {
			continue
		}
		if unit > 0xDBFF || i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		i += 6
	}
	return true
}

func boundedString(value string, minimum, maximum int) bool {
	return len(value) <= 4*maximum && utf8.ValidString(value) && utf8.RuneCountInString(value) >= minimum && utf8.RuneCountInString(value) <= maximum
}
