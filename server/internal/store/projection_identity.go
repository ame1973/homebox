package store

import (
	"encoding/json"
	"unicode/utf8"
)

// projectionPair matches canonical JSON string-array spelling (JSON.stringify),
// including literal non-ASCII, HTML characters and U+2028/U+2029. Go's default
// JSON encoder escapes some of those, which would split one logical identity.
func projectionPair(first, second string) string {
	if !validDomainID(first) || !validDomainID(second) {
		return ""
	}
	value := make([]byte, 0, len(first)+len(second)+7)
	value = append(value, '[')
	for index, part := range []string{first, second} {
		if index > 0 {
			value = append(value, ',')
		}
		value = append(value, '"')
		for _, character := range part {
			switch character {
			case '"', '\\':
				value = append(value, '\\', byte(character))
			case '\b':
				value = append(value, '\\', 'b')
			case '\t':
				value = append(value, '\\', 't')
			case '\n':
				value = append(value, '\\', 'n')
			case '\f':
				value = append(value, '\\', 'f')
			case '\r':
				value = append(value, '\\', 'r')
			default:
				if character < 0x20 {
					const hex = "0123456789abcdef"
					value = append(value, '\\', 'u', '0', '0', hex[character>>4], hex[character&15])
				} else {
					value = utf8.AppendRune(value, character)
				}
			}
		}
		value = append(value, '"')
	}
	return string(append(value, ']'))
}

func projectionPairParts(identity string) ([]string, bool) {
	// Two maximum-length IDs, each entirely escaped control characters, plus
	// quotes/comma/brackets. Bound decoding before allocating attacker input.
	if len(identity) > 2*128*6+7 {
		return nil, false
	}
	var parts []string
	if json.Unmarshal([]byte(identity), &parts) != nil || len(parts) != 2 ||
		!validDomainID(parts[0]) || !validDomainID(parts[1]) || projectionPair(parts[0], parts[1]) != identity {
		return nil, false
	}
	return parts, true
}

func validProjectionIdentity(kind, identity string) bool {
	switch kind {
	case "location", "location_event", "media_link", "category", "tag", "item", "barcode", "alias", "attribute", "ai_review":
		return validDomainID(identity)
	case "item_tag", "search_doc":
		parts, valid := projectionPairParts(identity)
		return valid && (kind == "item_tag" || parts[0] == "item" || parts[0] == "unit")
	default:
		return false
	}
}
