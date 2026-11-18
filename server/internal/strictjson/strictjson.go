// Package strictjson provides lossless JSON boundary checks for HTTP and
// future persisted-projection readers. These checks do not validate a schema.
package strictjson

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxSafeInteger int64 = 9007199254740991

// ValidUnicodeJSON rejects invalid UTF-8, malformed JSON and unpaired escaped
// surrogates that encoding/json would otherwise silently replace with U+FFFD.
func ValidUnicodeJSON(body []byte) bool {
	if !utf8.Valid(body) || !json.Valid(body) {
		return false
	}
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

// ExactSafeInteger accepts integral JSON decimals/exponents without float64
// rounding. It accepts JSON whitespace around the number; the byte limit still
// applies before trimming. Nonzero-exponent bounds keep hostile inputs cheap.
func ExactSafeInteger(raw []byte) (int64, bool) {
	if len(raw) == 0 || len(raw) > 128 || !json.Valid(raw) {
		return 0, false
	}
	raw = bytes.Trim(raw, " \t\r\n")
	if len(raw) == 0 || raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return 0, false
	}
	text := string(raw)
	coefficient := text
	index := strings.IndexAny(text, "eE")
	if index >= 0 {
		coefficient = text[:index]
	}
	zero := true
	for _, character := range coefficient {
		if character >= '1' && character <= '9' {
			zero = false
		}
	}
	if zero {
		return 0, true
	}
	if index >= 0 {
		exponent, err := strconv.ParseInt(text[index+1:], 10, 64)
		if err != nil || exponent < -256 || exponent > 256 {
			return 0, false
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok || !value.IsInt() || !value.Num().IsInt64() {
		return 0, false
	}
	number := value.Num().Int64()
	return number, number >= -MaxSafeInteger && number <= MaxSafeInteger
}
