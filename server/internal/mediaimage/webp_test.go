package mediaimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// These independent 2x3 rasters were produced with Pillow/libwebp 1.3.2.
const losslessWebP = "UklGRjYAAABXRUJQVlA4TCkAAAAvAYAAEC8w/wKCIv9HExAU+T+aQIDgzP+U4A0i6igIBNKQUgorHNH/WAA="
const lossyWebP = "UklGRmoAAABXRUJQVlA4IF4AAACwAgCdASoCAAMAAMASJagCdLoBJgMEA9f8EOzgmQAA/vKT/2r9Av+Vf/9WOey7nHOt+30f6UAJ7/37sfyK++N3+L//70dfyK++N3+L//9eZf9eZfFp6ynzkoWh8AAA"
const lossyAlphaWebP = "UklGRogAAABXRUJQVlA4WAoAAAAQAAAAAQAAAgAAQUxQSAcAAAAA/4D/AP9AAFZQOCBaAAAAsAMAnQEqAgADAADAEiWoAnS6AfgB+gEwA/gGEAev/dgAMDSy0AAA/QPp3b8Z/+/9lv/2xEaL/+l4PvTutm0CAx6d1s21V//ceX/4VWu0YoCd+0YpiN/zcAAA"
const animatedWebP = "UklGRqIAAABXRUJQVlA4WAoAAAASAAAAAQAAAgAAQU5JTQYAAAAAAAAAAABBTk1GRAAAAAAAAAAAAAEAAAIAAHgAAAJWUDhMKwAAAC8BgAAQLyAQSNofeo35FxAU/B+dQCAJbbPVg0xc7AAEApSxYoMHIvof7wAAQU5NRioAAAAAAAAAAAABAAACAAB4AAAAVlA4TBEAAAAvAYAAAAdQlFKUp/+BiOh/AAA="

func webpFixture(t testing.TB, encoded string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func webpChunk(name string, data []byte) []byte {
	result := make([]byte, 8+len(data)+(len(data)&1))
	copy(result, name)
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(data)))
	copy(result[8:], data)
	return result
}

func webpContainer(chunks ...[]byte) []byte {
	raw := []byte("RIFF\x00\x00\x00\x00WEBP")
	for _, chunk := range chunks {
		raw = append(raw, chunk...)
	}
	binary.LittleEndian.PutUint32(raw[4:8], uint32(len(raw)-8))
	return raw
}

func extendedLosslessWebP(t testing.TB, flags byte, extra ...[]byte) []byte {
	t.Helper()
	header := []byte{flags, 0, 0, 0, 1, 0, 0, 2, 0, 0}
	chunks := [][]byte{webpChunk("VP8X", header), webpFixture(t, losslessWebP)[12:]}
	chunks = append(chunks, extra...)
	return webpContainer(chunks...)
}

func TestWebPLosslessOrientationPreservesAlphaAndStripsMetadata(t *testing.T) {
	palette := []color.NRGBA{
		{R: 255, A: 255}, {G: 255, A: 128}, {B: 255, A: 255},
		{R: 255, G: 255, A: 0}, {R: 255, B: 255, A: 255}, {G: 255, B: 255, A: 64},
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, tc := range []struct {
			orientation uint16
			rows        []string
		}{
			{1, []string{"ab", "cd", "ef"}}, {2, []string{"ba", "dc", "fe"}},
			{3, []string{"fe", "dc", "ba"}}, {4, []string{"ef", "cd", "ab"}},
			{5, []string{"ace", "bdf"}}, {6, []string{"eca", "fdb"}},
			{7, []string{"fdb", "eca"}}, {8, []string{"bdf", "ace"}},
		} {
			t.Run(string(rune('0'+tc.orientation))+"/"+string([]byte{map[bool]byte{true: 'l', false: 'b'}[order == binary.LittleEndian]}), func(t *testing.T) {
				raw := extendedLosslessWebP(t, 0x18, webpChunk("EXIF", testEXIF(order, tc.orientation)))
				var full, thumbnail bytes.Buffer
				result, preview, err := SanitizeWithThumbnail(t.Context(), raw, "image/webp", &full, &thumbnail, Limits{})
				if err != nil {
					t.Fatal(err)
				}
				if result.MIMEType != "image/png" || result.Width != len(tc.rows[0]) || result.Height != len(tc.rows) || result.Bytes != int64(full.Len()) || result.SHA256 != sha256.Sum256(full.Bytes()) {
					t.Fatalf("sanitized WebP did not describe its actual PNG bytes: %+v", result)
				}
				decoded, err := png.Decode(bytes.NewReader(full.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				for y, row := range tc.rows {
					for x, label := range row {
						got := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
						want := palette[int(label-'a')]
						if got != want {
							t.Fatalf("orientation %d at %d,%d: got %v, want %v", tc.orientation, x, y, got, want)
						}
					}
				}
				if _, err = inspectPNG(full.Bytes()); err != nil || bytes.Contains(full.Bytes(), privateGPSMarker) || bytes.Contains(thumbnail.Bytes(), privateGPSMarker) {
					t.Fatal("GPS or malformed data survived WebP sanitization", err)
				}
				if _, err = jpeg.Decode(bytes.NewReader(thumbnail.Bytes())); err != nil || preview.MIMEType != "image/jpeg" || preview.Width != result.Width || preview.Height != result.Height || preview.SHA256 != sha256.Sum256(thumbnail.Bytes()) {
					t.Fatal("invalid sanitized WebP thumbnail", preview, err)
				}
			})
		}
	}
}

func TestWebPLossyAlphaAndDistinctMetadataProduceSafeOutput(t *testing.T) {
	for _, encoded := range []string{losslessWebP, lossyWebP} {
		result, output := sanitize(t, webpFixture(t, encoded), "image/webp", Limits{})
		if decoded, err := png.Decode(bytes.NewReader(output)); err != nil || result.MIMEType != "image/png" || decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 3 {
			t.Fatalf("simple WebP source was not re-encoded: %+v %v", result, err)
		}
	}
	raw := webpFixture(t, lossyAlphaWebP)
	result, output := sanitize(t, raw, "IMAGE/WEBP", Limits{})
	decoded, err := png.Decode(bytes.NewReader(output))
	if err != nil || result.MIMEType != "image/png" || result.Width != 2 || result.Height != 3 {
		t.Fatalf("lossy-alpha source was not decoded to PNG: %+v %v", result, err)
	}
	for index, alpha := range []uint8{255, 128, 255, 0, 255, 64} {
		got := color.NRGBAModel.Convert(decoded.At(index%2, index/2)).(color.NRGBA)
		if got.A != alpha {
			t.Fatalf("alpha %d: got %d, want %d", index, got.A, alpha)
		}
	}
	first := extendedLosslessWebP(t, 0x18, webpChunk("EXIF", testEXIF(binary.LittleEndian, 1)))
	otherEXIF := append([]byte{}, testEXIF(binary.LittleEndian, 1)...)
	otherEXIF[len(otherEXIF)-1] ^= 1
	second := extendedLosslessWebP(t, 0x18, webpChunk("EXIF", otherEXIF))
	one, a := sanitize(t, first, "image/webp", Limits{})
	two, b := sanitize(t, second, "image/webp", Limits{})
	if bytes.Equal(first, second) || one != two || !bytes.Equal(a, b) {
		t.Fatal("private metadata affected freshly encoded output")
	}
	wrapped := append([]byte("Exif\x00\x00"), testEXIF(binary.BigEndian, 6)...)
	extended := webpContainer(
		webpChunk("VP8X", []byte{0x3c, 0, 0, 0, 1, 0, 0, 2, 0, 0}),
		webpChunk("ICCP", []byte("private color profile")), webpFixture(t, losslessWebP)[12:],
		webpChunk("EXIF", wrapped), webpChunk("XMP ", []byte("private coordinates")), webpChunk("JUNK", []byte("private unknown data")),
	)
	oriented, safe := sanitize(t, extended, "image/webp", Limits{})
	if oriented.Width != 3 || oriented.Height != 2 || bytes.Contains(safe, []byte("private")) {
		t.Fatal("extended metadata was not stripped or EXIF orientation was lost", oriented)
	}
}

func TestWebPRejectsMalformedAnimatedAndMismatchedInputWithoutOutput(t *testing.T) {
	plain := webpFixture(t, losslessWebP)
	withEXIF := extendedLosslessWebP(t, 0x18, webpChunk("EXIF", testEXIF(binary.LittleEndian, 1)))
	badSize := append([]byte{}, plain...)
	binary.LittleEndian.PutUint32(badSize[4:8], uint32(len(badSize)))
	badReserved := extendedLosslessWebP(t, 0x50)
	badPadding := webpContainer(webpChunk("VP8X", []byte{0x18, 0, 0, 0, 1, 0, 0, 2, 0, 0}), plain[12:], webpChunk("EXIF", testEXIF(binary.LittleEndian, 1)))
	badPadding[len(badPadding)-1] = 1
	overflowChunk := append([]byte{}, plain...)
	binary.LittleEndian.PutUint32(overflowChunk[16:20], ^uint32(0))
	mismatchedCanvas := webpContainer(webpChunk("VP8X", []byte{0x10, 0, 0, 0, 0, 0, 0, 0, 0, 0}), plain[12:])
	wrongAlpha := webpContainer(webpChunk("VP8X", []byte{0x10, 0, 0, 0, 1, 0, 0, 2, 0, 0}), webpChunk("ALPH", []byte{0, 1}), webpFixture(t, lossyWebP)[12:])
	for _, tc := range []struct {
		name, mime string
		raw        []byte
	}{
		{"invalid RIFF", "image/webp", []byte("RIFF....WEBP")},
		{"mismatched JPEG", "image/jpeg", plain}, {"mismatched PNG", "image/png", plain},
		{"mismatched WebP", "image/webp", encodePNG(t, sampleImage())},
		{"truncated", "image/webp", plain[:len(plain)-1]}, {"trailing file", "image/webp", append(append([]byte{}, plain...), plain...)},
		{"wrong RIFF length", "image/webp", badSize}, {"overflow chunk length", "image/webp", overflowChunk},
		{"canvas and bitstream disagree", "image/webp", mismatchedCanvas}, {"short uncompressed alpha", "image/webp", wrongAlpha},
		{"reserved extended bits", "image/webp", badReserved},
		{"duplicate extended header", "image/webp", webpContainer(webpChunk("VP8X", make([]byte, 10)), webpChunk("VP8X", make([]byte, 10)), plain[12:])},
		{"animation", "image/webp", webpFixture(t, animatedWebP)},
		{"animation chunk after still", "image/webp", extendedLosslessWebP(t, 0x10, webpChunk("ANMF", []byte{0, 0}))},
		{"duplicate image", "image/webp", webpContainer(plain[12:], plain[12:])},
		{"duplicate EXIF", "image/webp", extendedLosslessWebP(t, 0x18, webpChunk("EXIF", testEXIF(binary.LittleEndian, 1)), webpChunk("EXIF", testEXIF(binary.LittleEndian, 1)))},
		{"invalid EXIF", "image/webp", extendedLosslessWebP(t, 0x18, webpChunk("EXIF", []byte("II")))},
		{"EXIF without flag", "image/webp", extendedLosslessWebP(t, 0x10, webpChunk("EXIF", testEXIF(binary.LittleEndian, 1)))},
		{"missing declared EXIF", "image/webp", extendedLosslessWebP(t, 0x18)},
		{"nonzero padding", "image/webp", badPadding},
		{"extra EXIF outside RIFF", "image/webp", append(append([]byte{}, withEXIF...), 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var full, thumbnail bytes.Buffer
			if _, _, err := SanitizeWithThumbnail(t.Context(), tc.raw, tc.mime, &full, &thumbnail, Limits{}); !errors.Is(err, ErrInvalid) || full.Len() != 0 || thumbnail.Len() != 0 {
				t.Fatalf("unsafe WebP accepted or wrote output: %v", err)
			}
		})
	}
	var output bytes.Buffer
	oversizedCanvas := webpContainer(webpChunk("VP8X", []byte{0x10, 0, 0, 0, 0, 0x40, 0, 0, 0, 0}), plain[12:])
	if _, err := Sanitize(t.Context(), oversizedCanvas, "image/webp", &output, Limits{}); !errors.Is(err, ErrTooLarge) || output.Len() != 0 {
		t.Fatalf("oversized WebP canvas reached pixel decoding: %v", err)
	}
	if _, err := Sanitize(t.Context(), plain, "image/webp", &output, Limits{Dimension: 1}); !errors.Is(err, ErrTooLarge) || output.Len() != 0 {
		t.Fatalf("WebP dimensions bypassed the configured limit: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Sanitize(ctx, plain, "image/webp", &output, Limits{}); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatalf("canceled WebP processing wrote output: %v", err)
	}
}
