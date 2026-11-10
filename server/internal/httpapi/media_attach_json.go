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
	if raw, present := values["is_cover"]; present {
		var value bool
		if len(raw) == 0 || raw[0] != 't' && raw[0] != 'f' || json.Unmarshal(raw, &value) != nil {
			return invalid()
		}
		input.IsCover = &value
	}
	if raw, present := values["sort_order"]; present {
		value, ok := exactSafeInteger(raw)
		if !ok || value < 0 {
			return invalid()
		}
		input.SortOrder = &value
	}
	if raw, present := values["description"]; present {
		input.Description, ok = jsonString(raw, 0, 10000)
		if !ok {
			return invalid()
		}
	}
	return input, true
}
