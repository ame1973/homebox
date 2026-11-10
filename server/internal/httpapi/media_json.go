package httpapi

import (
	"encoding/json"
	"net/http"

	"homebox/internal/store"
)

const mediaLinkBodyLimit = 128 << 10

func readMediaLinkCreate(w http.ResponseWriter, r *http.Request, requestID string) (store.MediaLinkCreate, bool) {
	values, ok := readJSONObject(w, r, requestID, mediaLinkBodyLimit,
		"item_id", "item_unit_id", "location_id", "owner_revision", "is_cover", "sort_order", "description")
	if !ok {
		return store.MediaLinkCreate{}, false
	}
	invalid := func() (store.MediaLinkCreate, bool) {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Media input is outside its allowed bounds.", requestID)
		return store.MediaLinkCreate{}, false
	}
	var input store.MediaLinkCreate
	owners := 0
	for _, field := range []struct {
		name        string
		destination **string
	}{{"item_id", &input.ItemID}, {"item_unit_id", &input.ItemUnitID}, {"location_id", &input.LocationID}} {
		if raw, present := values[field.name]; present {
			value, ok := jsonString(raw, 1, 128)
			if !ok {
				return invalid()
			}
			*field.destination = value
			owners++
		}
	}
	input.OwnerRevision, ok = exactSafeInteger(values["owner_revision"])
	if !ok || input.OwnerRevision < 1 || owners != 1 {
		return invalid()
	}
	metadata, ok := mediaLinkMetadata(values)
	if !ok {
		return invalid()
	}
	input.IsCover, input.SortOrder, input.Description = metadata.IsCover, metadata.SortOrder, metadata.Description
	return input, true
}

func readMediaLinkPatch(w http.ResponseWriter, r *http.Request, requestID string) (store.MediaLinkPatch, bool) {
	values, ok := readJSONObject(w, r, requestID, mediaLinkBodyLimit, "is_cover", "sort_order", "description")
	if !ok {
		return store.MediaLinkPatch{}, false
	}
	input, valid := mediaLinkMetadata(values)
	if !valid || len(values) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Media input is outside its allowed bounds.", requestID)
		return store.MediaLinkPatch{}, false
	}
	return input, true
}

func mediaLinkMetadata(values map[string]json.RawMessage) (store.MediaLinkPatch, bool) {
	var input store.MediaLinkPatch
	if raw, present := values["is_cover"]; present {
		var value bool
		if len(raw) == 0 || raw[0] != 't' && raw[0] != 'f' || json.Unmarshal(raw, &value) != nil {
			return store.MediaLinkPatch{}, false
		}
		input.IsCover = &value
	}
	if raw, present := values["sort_order"]; present {
		value, ok := exactSafeInteger(raw)
		if !ok || value < 0 {
			return store.MediaLinkPatch{}, false
		}
		input.SortOrder = &value
	}
	if raw, present := values["description"]; present {
		value, ok := jsonString(raw, 0, 10000)
		if !ok {
			return store.MediaLinkPatch{}, false
		}
		input.Description = value
	}
	return input, true
}
