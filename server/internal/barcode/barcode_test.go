package barcode

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

var validNumericCodes = []struct{ symbology, raw string }{
	{"EAN_13", "4006381333931"}, {"EAN_13", "9780201379624"}, {"EAN_13", "5901234123457"},
	{"EAN_13", "4901234567894"}, {"EAN_13", "0123456789012"}, {"EAN_13", "0000000000000"},
	{"EAN_8", "96385074"}, {"EAN_8", "55123457"}, {"EAN_8", "00000000"},
	{"UPC_A", "036000291452"}, {"UPC_A", "042100005264"}, {"UPC_A", "012345000065"},
	{"UPC_A", "000000000000"},
}

func TestValidSymbologyRecognizesOnlyRetainedNamespaces(t *testing.T) {
	for _, value := range []string{"EAN_13", "EAN_8", "UPC_A", "UPC_E", "CODE_128", "QR_CODE", "OTHER"} {
		if !ValidSymbology(value) {
			t.Fatalf("supported namespace rejected: %q", value)
		}
	}
	for _, value := range []string{"", "ean_13", "EAN_13 ", "EAN_13\x00", "JAN", string([]byte{0xff})} {
		if ValidSymbology(value) {
			t.Fatalf("unknown namespace accepted: %q", value)
		}
	}
}

func TestNormalizePreservesValidatedGTINStringsAndLeadingZeros(t *testing.T) {
	for _, code := range validNumericCodes {
		t.Run(code.symbology+"/"+code.raw, func(t *testing.T) {
			t.Parallel()
			normalized, err := Normalize(code.symbology, code.raw)
			if err != nil || normalized != code.raw {
				t.Fatalf("validated identifier changed or failed: %q, %v", normalized, err)
			}
		})
	}
}

func TestNormalizeRejectsEverySingleDigitErrorInEANAndUPCA(t *testing.T) {
	for _, code := range validNumericCodes {
		for position := range len(code.raw) {
			for delta := byte(1); delta <= 9; delta++ {
				changed := []byte(code.raw)
				changed[position] = '0' + (changed[position]-'0'+delta)%10
				if normalized, err := Normalize(code.symbology, string(changed)); !errors.Is(err, ErrInvalid) || normalized != "" {
					t.Fatalf("accepted a single-digit checksum error for %s at position %d", code.symbology, position)
				}
			}
		}
	}
}

func TestUPCEExpansionValidatesAllCompressionCasesWithoutChangingTheStoredNamespace(t *testing.T) {
	// Fixed expansions/check digits were calculated independently with left-side
	// UPC-A weights, including the published 04252614/042100005264 example.
	for _, code := range []struct{ raw, expanded string }{
		{"04252614", "042100005264"}, {"01234565", "012345000065"},
		{"01234505", "012000003455"}, {"01234514", "012100003454"},
		{"01234523", "012200003453"}, {"01234531", "012300000451"},
		{"01234543", "012340000053"}, {"01234558", "012345000058"},
		{"01234596", "012345000096"}, {"11234538", "112300000458"},
	} {
		t.Run(code.raw, func(t *testing.T) {
			t.Parallel()
			if expanded := expandUPCE(code.raw); expanded != code.expanded {
				t.Fatalf("expanded %q, want %q", expanded, code.expanded)
			}
			if normalized, err := Normalize("UPC_A", code.expanded); err != nil || normalized != code.expanded {
				t.Fatalf("independent UPC-A vector failed: %v", err)
			}
			if normalized, err := Normalize("UPC_E", code.raw); err != nil || normalized != code.raw {
				t.Fatalf("UPC-E normalization lost compressed identity: %q, %v", normalized, err)
			}
			for delta := byte(1); delta <= 9; delta++ {
				changed := []byte(code.raw)
				changed[7] = '0' + (changed[7]-'0'+delta)%10
				if normalized, err := Normalize("UPC_E", string(changed)); !errors.Is(err, ErrInvalid) || normalized != "" {
					t.Fatal("accepted an invalid UPC-E check digit")
				}
			}
		})
	}
}

func TestNormalizeRejectsInvalidWidthAlphabetEncodingAndImplicitRepairs(t *testing.T) {
	for _, code := range []struct{ symbology, raw string }{
		{"", "4006381333931"}, {"ean_13", "4006381333931"}, {"JAN", "4901234567894"},
		{" EAN_13", "4006381333931"}, {"EAN_13", ""}, {"EAN_13", "4006381333932"},
		{"EAN_13", "400638133393"}, {"EAN_13", "04006381333931"},
		{"EAN_13", " 4006381333931"}, {"EAN_13", "4006381333931 "},
		{"EAN_13", "4006381333931\n"}, {"EAN_13", "4006381333931\r\n"},
		{"EAN_13", "４006381333931"}, {"EAN_13", "٤006381333931"},
		{"EAN_13", "40063813339-1"}, {"EAN_13", "+006381333931"},
		{"EAN_13", "40063813339\x001"}, {"EAN_8", "9638507"}, {"EAN_8", "96385075"},
		{"UPC_A", "03600029145"}, {"UPC_A", "036000291453"},
		{"UPC_E", "123456"}, {"UPC_E", "0123456"}, {"UPC_E", "012345655"},
		{"UPC_E", "21234565"}, {"UPC_E", "91234565"}, {"UPC_E", "01234566"},
		{"CODE_128", "非 ASCII"}, {"CODE_128", "é"}, {"CODE_128", string([]byte{0x80})},
		{"OTHER", string([]byte{0xff})}, {"QR_CODE", string([]byte{0xed, 0xa0, 0x80})},
		{"OTHER", ""}, {"QR_CODE", strings.Repeat("😀", 513)},
		{"CODE_128", strings.Repeat("A", 513)}, {"OTHER", strings.Repeat("A", 2049)},
	} {
		normalized, err := Normalize(code.symbology, code.raw)
		if !errors.Is(err, ErrInvalid) || normalized != "" || err.Error() != "invalid product barcode" {
			t.Fatalf("invalid %s did not fail without retaining the input", code.symbology)
		}
	}
}

func TestNormalizeKeepsOpaqueTextAndASCIIControlsWithoutNavigationOrUnicodeFolding(t *testing.T) {
	ascii := make([]byte, 128)
	for i := range ascii {
		ascii[i] = byte(i)
	}
	for _, code := range []struct{ symbology, raw string }{
		{"CODE_128", string(ascii)}, {"CODE_128", strings.Repeat("A", 512)},
		{"CODE_128", "  000123 ASCII  "}, {"CODE_128", "\x00\x1d\x7f"},
		{"QR_CODE", strings.Repeat("😀", 512)}, {"OTHER", strings.Repeat("a", 512)},
		{"OTHER", "  é / É / é / Ａ  "}, {"QR_CODE", "https://outside.invalid/路徑?do=not-navigate#文字"},
		{"QR_CODE", "https://homebox.invalid/l/location"}, {"OTHER", "\x00  \n"},
		{"OTHER", "�"}, {"OTHER", " "}, {"QR_CODE", "\n"},
	} {
		if normalized, err := Normalize(code.symbology, code.raw); err != nil || normalized != code.raw {
			t.Fatalf("opaque %s changed: %v", code.symbology, err)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, code := range validNumericCodes {
		f.Add(code.symbology, code.raw)
	}
	for _, code := range []struct{ symbology, raw string }{
		{"UPC_E", "04252614"}, {"UPC_E", "01234565"}, {"UPC_E", "01234531"},
		{"CODE_128", "\x00GS1\x1d00123"}, {"QR_CODE", "家庭 é"}, {"OTHER", strings.Repeat("😀", 512)},
		{"EAN_13", "4006381333932"}, {"OTHER", string([]byte{0xff})}, {"unknown", "value"},
	} {
		f.Add(code.symbology, code.raw)
	}
	f.Fuzz(func(t *testing.T, symbology, raw string) {
		normalized, err := Normalize(symbology, raw)
		if err != nil {
			if !errors.Is(err, ErrInvalid) || normalized != "" {
				t.Fatal("failure retained a value or returned an unsafe error")
			}
			return
		}
		if normalized != raw || !utf8.ValidString(raw) || utf8.RuneCountInString(raw) < 1 || utf8.RuneCountInString(raw) > 512 {
			t.Fatal("success changed or accepted invalid text")
		}
		switch symbology {
		case "EAN_13", "EAN_8", "UPC_A", "UPC_E":
			if len(raw) != map[string]int{"EAN_13": 13, "EAN_8": 8, "UPC_A": 12, "UPC_E": 8}[symbology] {
				t.Fatal("numeric width changed")
			}
			for i := range len(raw) {
				if raw[i] < '0' || raw[i] > '9' {
					t.Fatal("numeric alphabet changed")
				}
			}
		case "CODE_128":
			for i := range len(raw) {
				if raw[i] > 0x7f {
					t.Fatal("non-ASCII Code128 accepted")
				}
			}
		case "QR_CODE", "OTHER":
		default:
			t.Fatal("unsupported symbology accepted")
		}
		if again, retry := Normalize(symbology, normalized); retry != nil || again != normalized {
			t.Fatal("validated normalization is not idempotent")
		}
	})
}

func BenchmarkNormalize(b *testing.B) {
	for _, code := range []struct{ symbology, raw string }{
		{"EAN_13", "4006381333931"}, {"UPC_E", "04252614"},
		{"CODE_128", strings.Repeat("A", 512)}, {"QR_CODE", strings.Repeat("😀", 512)},
	} {
		b.Run(code.symbology, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if value, err := Normalize(code.symbology, code.raw); err != nil || value != code.raw {
					b.Fatal("valid benchmark code failed")
				}
			}
		})
	}
}
