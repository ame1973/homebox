// Package barcode validates product identifiers without changing their meaning.
package barcode

import (
	"errors"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid product barcode")

// ValidSymbology identifies supported namespaces without applying new-write
// checksum or alphabet rules to retained historical values.
func ValidSymbology(value string) bool {
	switch value {
	case "EAN_13", "EAN_8", "UPC_A", "UPC_E", "CODE_128", "QR_CODE", "OTHER":
		return true
	default:
		return false
	}
}

// Normalize returns the exact validated string within its original symbology.
// Leading zeros, case, whitespace and Unicode normalization are never repaired.
// This pure operation does not identify a unique item/unit or authorize a write.
func Normalize(symbology, raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 512*utf8.UTFMax || !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > 512 {
		return "", ErrInvalid
	}
	length := 0
	switch symbology {
	case "EAN_13":
		length = 13
	case "EAN_8", "UPC_E":
		length = 8
	case "UPC_A":
		length = 12
	case "CODE_128":
		for i := range len(raw) {
			if raw[i] > 0x7f {
				return "", ErrInvalid
			}
		}
		return raw, nil
	case "QR_CODE", "OTHER":
		return raw, nil
	default:
		return "", ErrInvalid
	}
	if len(raw) != length {
		return "", ErrInvalid
	}
	for i := range len(raw) {
		if raw[i] < '0' || raw[i] > '9' {
			return "", ErrInvalid
		}
	}
	digits := raw
	if symbology == "UPC_E" {
		if raw[0] != '0' && raw[0] != '1' {
			return "", ErrInvalid
		}
		digits = expandUPCE(raw)
	}
	if !validCheckDigit(digits) {
		return "", ErrInvalid
	}
	return raw, nil
}

// The alternating GTIN weights start at three immediately before the check digit.
// Callers have already validated the fixed ASCII numeric width.
func validCheckDigit(digits string) bool {
	sum, weight := 0, 3
	for i := len(digits) - 2; i >= 0; i-- {
		sum += int(digits[i]-'0') * weight
		weight = 4 - weight
	}
	return (10-sum%10)%10 == int(digits[len(digits)-1]-'0')
}

// Expand only for checksum validation. Returning this representation as the
// normalized value would silently change the original UPC-E namespace/string.
func expandUPCE(raw string) string {
	digits := raw[1:7]
	switch digits[5] {
	case '0', '1', '2':
		return raw[:1] + digits[:2] + digits[5:6] + "0000" + digits[2:5] + raw[7:]
	case '3':
		return raw[:1] + digits[:3] + "00000" + digits[3:5] + raw[7:]
	case '4':
		return raw[:1] + digits[:4] + "00000" + digits[4:5] + raw[7:]
	default:
		return raw[:1] + digits[:5] + "0000" + digits[5:6] + raw[7:]
	}
}
