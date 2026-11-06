package currency

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMinorUnitsMatchesPinnedOfficialDataWithoutGuessingUnsupportedCodes(t *testing.T) {
	data, err := os.ReadFile("testdata/iso4217-2026-09-17.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Publication string            `json:"publication_date"`
		SHA         string            `json:"source_sha256"`
		Groups      map[string]string `json:"by_minor_units"`
		Undefined   string            `json:"undefined_minor_units"`
	}
	if err = json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	if source.Publication != PublicationDate || source.SHA != "33139b438657d1cee116ba737807ea71d19d6de4b90f799a09c56f0cc6a1b0ff" {
		t.Fatal("currency snapshot identity changed without reviewed policy update")
	}
	expected := make(map[string]int)
	for text, codes := range source.Groups {
		exponent, err := strconv.Atoi(text)
		if err != nil {
			t.Fatal(err)
		}
		for _, code := range strings.Fields(codes) {
			if _, exists := expected[code]; exists {
				t.Fatal("duplicate official code", code)
			}
			expected[code] = exponent
			if actual, ok := MinorUnits(code); !ok || actual != exponent {
				t.Fatal("registered minor-unit definition changed", code, actual, exponent)
			}
		}
	}
	if len(expected) != 165 {
		t.Fatal("incomplete official finite-minor-unit snapshot", len(expected))
	}
	for a := byte('A'); a <= 'Z'; a++ {
		for b := byte('A'); b <= 'Z'; b++ {
			for c := byte('A'); c <= 'Z'; c++ {
				code := string([]byte{a, b, c})
				value, ok := MinorUnits(code)
				wanted, exists := expected[code]
				if ok != exists || ok && value != wanted {
					t.Fatal("unknown code was guessed or registered code omitted", code)
				}
			}
		}
	}
	for _, code := range append(strings.Fields(source.Undefined), "BGN", "DEM", "ZZZ", "", "jpy", " JPY", "JPY\n", "JP\x00", "日本円") {
		if _, ok := MinorUnits(code); ok {
			t.Fatal("unsupported spelling/history/undefined exponent accepted for new money", code)
		}
	}
}
