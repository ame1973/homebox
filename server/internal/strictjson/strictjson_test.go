package strictjson

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidUnicodeJSONRejectsLossyDecoding(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `"plain"`, `"\u0000"`, `"😀"`, `"\\ud800"`, `"\ud83d\ude00"`,
		`{"家":"😀","text":"𝄞"}`, `"\uD834\uDD1E"`, `"�"`,
	} {
		if !ValidUnicodeJSON([]byte(body)) {
			t.Fatalf("valid JSON was rejected: %q", body)
		}
	}
	for _, body := range []string{
		``, `{`, `{} {}`, `"\ud800"`, `"\udc00"`, `"\ud800A"`, `"\ud800\\udc00"`,
		`{"x":"\udbff"}`, `"\ud800\ud800"`, "\"\xff\"", `"\uXXXX"`,
	} {
		if ValidUnicodeJSON([]byte(body)) {
			t.Fatalf("invalid or lossy JSON was accepted: %q", body)
		}
	}
}

func TestExactSafeIntegerRetainsJSONIntegerSemantics(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int64
	}{
		{"0", 0}, {"-0", 0}, {" 0 ", 0}, {" \t1.0\n", 1}, {"0e999999999999999999999999", 0}, {"-0.000e-99999999999999999999", 0},
		{"1.0", 1}, {"10e-1", 1}, {"1E+3", 1000},
		{"9007199254740991", MaxSafeInteger}, {"-9007199254740991", -MaxSafeInteger},
		{"9007199254740991.000", MaxSafeInteger},
	} {
		got, ok := ExactSafeInteger([]byte(test.raw))
		if !ok || got != test.want {
			t.Fatalf("integer %q: got %d, %v; want %d", test.raw, got, ok, test.want)
		}
	}
	for _, raw := range []string{
		"", "null", "true", `"1"`, "1 2", "01", "+1", "1.", "1.00000000000000001",
		"9007199254740992", "-9007199254740992", "-15e-1", "1e257", "1e-257", "1e99999999999999999999",
		strings.Repeat("0", 129), "0." + strings.Repeat("0", 127),
	} {
		if _, ok := ExactSafeInteger([]byte(raw)); ok {
			t.Fatalf("invalid or unsafe number was accepted: %q", raw)
		}
	}
}

func FuzzValidUnicodeJSON(f *testing.F) {
	for _, seed := range []string{`{}`, `"😀"`, `"\ud800"`, `"\\ud800"`, "\"\xff\""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		body := []byte(value)
		if ValidUnicodeJSON(body) && (!utf8.Valid(body) || !json.Valid(body)) {
			t.Fatal("accepted invalid JSON or UTF-8")
		}
	})
}

func FuzzExactSafeInteger(f *testing.F) {
	for _, seed := range []string{"0", "-0", "1.0", "10e-1", "1.00000000000000001", "9007199254740991", "1e999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		value, ok := ExactSafeInteger([]byte(raw))
		if !ok {
			return
		}
		if len(raw) > 128 || !json.Valid([]byte(raw)) || value < -MaxSafeInteger || value > MaxSafeInteger {
			t.Fatal("accepted an invalid or unsafe numeric value")
		}
		// Avoid asking big.Rat to materialize an unbounded exponent. The parser
		// has already established that any such accepted value is exactly zero.
		number := strings.Trim(raw, " \t\r\n")
		if index := strings.IndexAny(number, "eE"); index >= 0 {
			exponent, err := strconv.ParseInt(number[index+1:], 10, 64)
			if err != nil || exponent < -256 || exponent > 256 {
				if value != 0 {
					t.Fatal("an unbounded exponent produced a nonzero result")
				}
				for _, digit := range number[:index] {
					if digit >= '1' && digit <= '9' {
						t.Fatal("a nonzero coefficient bypassed the exponent limit")
					}
				}
				return
			}
		}
		actual, ok := new(big.Rat).SetString(number)
		if !ok || !actual.IsInt() || !actual.Num().IsInt64() || actual.Num().Int64() != value {
			t.Fatal("numeric value changed during exact parsing")
		}
	})
}
