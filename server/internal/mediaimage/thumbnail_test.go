package mediaimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
	"time"
)

func TestSanitizeWithThumbnailPreservesOrientationAndStripsPrivateMetadata(t *testing.T) {
	palette := []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
		{R: 255, G: 255, A: 255}, {R: 255, B: 255, A: 255}, {G: 255, B: 255, A: 255}}
	original := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for y := range 60 {
		for x := range 40 {
			original.SetRGBA(x, y, palette[(y/20)*2+x/20])
		}
	}
	for _, tc := range []struct {
		orientation uint16
		rows        []string
	}{
		{1, []string{"ab", "cd", "ef"}}, {2, []string{"ba", "dc", "fe"}},
		{3, []string{"fe", "dc", "ba"}}, {4, []string{"ef", "cd", "ab"}},
		{5, []string{"ace", "bdf"}}, {6, []string{"eca", "fdb"}},
		{7, []string{"fdb", "eca"}}, {8, []string{"bdf", "ace"}},
	} {
		t.Run(string(rune('0'+tc.orientation)), func(t *testing.T) {
			raw := insertPNGChunk(encodePNG(t, original), "eXIf", testEXIF(binary.LittleEndian, tc.orientation), false)
			raw = insertPNGChunk(raw, "tEXt", append([]byte("Location\x00"), privateGPSMarker...), true)
			var full, thumb bytes.Buffer
			fullResult, thumbResult, err := SanitizeWithThumbnail(t.Context(), raw, "image/png", &full, &thumb, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			expected, expectedBytes := sanitize(t, raw, "image/png", Limits{})
			if fullResult != expected || !bytes.Equal(full.Bytes(), expectedBytes) {
				t.Fatal("thumbnail generation changed sanitized original")
			}
			if thumbResult.MIMEType != "image/jpeg" || thumbResult.Width != len(tc.rows[0])*20 || thumbResult.Height != len(tc.rows)*20 ||
				thumbResult.Bytes != int64(thumb.Len()) || thumbResult.SHA256 != sha256.Sum256(thumb.Bytes()) {
				t.Fatalf("thumbnail result does not describe bytes: %+v", thumbResult)
			}
			if bytes.Contains(full.Bytes(), privateGPSMarker) || bytes.Contains(thumb.Bytes(), privateGPSMarker) || bytes.Contains(thumb.Bytes(), []byte("Exif\x00\x00")) {
				t.Fatal("source metadata survived re-encoding")
			}
			orientation, err := inspectJPEG(thumb.Bytes())
			if err != nil || orientation != 1 {
				t.Fatalf("thumbnail is not a complete metadata-free JPEG: %d %v", orientation, err)
			}
			decoded, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			for y, row := range tc.rows {
				for x, label := range row {
					got := color.RGBAModel.Convert(decoded.At(x*20+10, y*20+10)).(color.RGBA)
					want := palette[int(label-'a')]
					for _, pair := range [][2]uint8{{got.R, want.R}, {got.G, want.G}, {got.B, want.B}} {
						if delta := int(pair[0]) - int(pair[1]); delta < -32 || delta > 32 {
							t.Fatalf("orientation %d (%d,%d): got %v, want %v", tc.orientation, x, y, got, want)
						}
					}
				}
			}
		})
	}
}

func TestThumbnailFitsBoundsWithoutUpscalingAndCompositesTransparency(t *testing.T) {
	for _, tc := range []struct{ width, height, wantWidth, wantHeight int }{
		{1024, 256, 512, 128}, {256, 1024, 128, 512}, {3, 2, 3, 2},
		{1, 1024, 1, 512}, {1024, 1, 512, 1}, {1001, 1000, 512, 511},
		{1, MaxDimension, 1, 512}, {MaxDimension, 1, 512, 1},
	} {
		img := image.NewNRGBA(image.Rect(0, 0, tc.width, tc.height))
		var full, thumb bytes.Buffer
		fullResult, result, err := SanitizeWithThumbnail(t.Context(), encodePNG(t, img), "image/png", &full, &thumb, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
		if err != nil || result.Width != tc.wantWidth || result.Height != tc.wantHeight || decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height ||
			result.Width > MaxThumbnailDimension || result.Height > MaxThumbnailDimension || fullResult.Width != tc.width || fullResult.Height != tc.height {
			t.Fatalf("source %dx%d: original %+v thumbnail %+v decode %v", tc.width, tc.height, fullResult, result, err)
		}
		white := color.RGBAModel.Convert(decoded.At(0, 0)).(color.RGBA)
		if white.R < 250 || white.G < 250 || white.B < 250 || white.A != 255 {
			t.Fatalf("transparent source did not composite onto white: %v", white)
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	for y := range 32 {
		for x := 32; x < 64; x++ {
			img.SetNRGBA(x, y, color.NRGBA{B: 255, A: 128})
		}
	}
	var thumb bytes.Buffer
	_, _, err := SanitizeWithThumbnail(t.Context(), encodePNG(t, img), "image/png", io.Discard, &thumb, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got := color.RGBAModel.Convert(decoded.At(48, 16)).(color.RGBA)
	if got.R < 115 || got.R > 140 || got.G < 115 || got.G > 140 || got.B < 245 {
		t.Fatalf("partial alpha was not composited onto white: %v", got)
	}
}

func TestSanitizeWithThumbnailJPEGOrientationAndGPS(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := range 20 {
		for x := range 40 {
			value := color.RGBA{R: 255, A: 255}
			if x >= 20 {
				value = color.RGBA{B: 255, A: 255}
			}
			img.SetRGBA(x, y, value)
		}
	}
	raw := insertJPEGEXIF(encodeJPEG(t, img), testEXIF(binary.LittleEndian, 6))
	expected, expectedBytes := sanitize(t, raw, "image/jpeg", Limits{})
	var full, thumb bytes.Buffer
	original, preview, err := SanitizeWithThumbnail(t.Context(), raw, "image/jpeg", &full, &thumb, Limits{})
	if err != nil || original != expected || !bytes.Equal(full.Bytes(), expectedBytes) || preview.Width != 20 || preview.Height != 40 {
		t.Fatalf("JPEG original/thumbnail: %+v %+v %v", original, preview, err)
	}
	if bytes.Contains(thumb.Bytes(), privateGPSMarker) || bytes.Contains(thumb.Bytes(), []byte("Exif\x00\x00")) {
		t.Fatal("JPEG thumbnail retained private metadata")
	}
	orientation, err := inspectJPEG(thumb.Bytes())
	if err != nil || orientation != 1 || preview.SHA256 != sha256.Sum256(thumb.Bytes()) || preview.Bytes != int64(thumb.Len()) {
		t.Fatalf("invalid JPEG thumbnail: %+v orientation %d: %v", preview, orientation, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	top := color.RGBAModel.Convert(decoded.At(10, 5)).(color.RGBA)
	bottom := color.RGBAModel.Convert(decoded.At(10, 35)).(color.RGBA)
	if top.R < 200 || top.B > 40 || bottom.B < 200 || bottom.R > 40 {
		t.Fatalf("JPEG thumbnail did not rotate: top %v, bottom %v", top, bottom)
	}
}

func TestThumbnailCompositesSixteenBitAlphaWithoutChangingOriginal(t *testing.T) {
	colors := []color.NRGBA64{
		{R: 65535, A: 0},
		{R: 10000, G: 40000, B: 60000, A: 32768},
		{R: 50000, G: 12000, B: 33000, A: 65535},
	}
	img := image.NewNRGBA64(image.Rect(0, 0, len(colors)*32, 32))
	for y := range 32 {
		for x := range len(colors) * 32 {
			img.SetNRGBA64(x, y, colors[x/32])
		}
	}
	var full, thumb bytes.Buffer
	_, _, err := SanitizeWithThumbnail(t.Context(), encodePNG(t, img), "image/png", &full, &thumb, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	original, err := png.Decode(bytes.NewReader(full.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for i, source := range colors {
		point := image.Pt(i*32+16, 16)
		if got := color.NRGBA64Model.Convert(original.At(point.X, point.Y)).(color.NRGBA64); got != source {
			t.Fatalf("original lost 16-bit alpha at %v: got %v, want %v", point, got, source)
		}
		got := color.RGBAModel.Convert(preview.At(point.X, point.Y)).(color.RGBA)
		for _, pair := range [][2]uint16{{uint16(got.R), source.R}, {uint16(got.G), source.G}, {uint16(got.B), source.B}} {
			mixed := (uint64(pair[1])*uint64(source.A) + 65535*uint64(65535-source.A) + 32767) / 65535
			want := int((mixed + 128) / 257)
			if delta := int(pair[0]) - want; delta < -4 || delta > 4 {
				t.Fatalf("16-bit compositing at %v: got %v, expected channel %d", point, got, want)
			}
		}
	}
}

func TestThumbnailDigestDoesNotIdentifyRawRequest(t *testing.T) {
	encoded := encodePNG(t, sampleImage())
	firstEXIF := testEXIF(binary.LittleEndian, 1)
	secondEXIF := append([]byte{}, firstEXIF...)
	secondEXIF[len(secondEXIF)-1] ^= 1
	firstRaw := insertPNGChunk(encoded, "eXIf", firstEXIF, false)
	secondRaw := insertPNGChunk(encoded, "eXIf", secondEXIF, false)
	if sha256.Sum256(firstRaw) == sha256.Sum256(secondRaw) {
		t.Fatal("fixture did not change the raw private metadata")
	}
	var firstFull, firstThumb, secondFull, secondThumb bytes.Buffer
	first, firstPreview, err := SanitizeWithThumbnail(t.Context(), firstRaw, "image/png", &firstFull, &firstThumb, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	second, secondPreview, err := SanitizeWithThumbnail(t.Context(), secondRaw, "image/png", &secondFull, &secondThumb, Limits{})
	if err != nil || first != second || firstPreview != secondPreview || !bytes.Equal(firstFull.Bytes(), secondFull.Bytes()) || !bytes.Equal(firstThumb.Bytes(), secondThumb.Bytes()) {
		t.Fatalf("private metadata changed sanitized bytes: %+v %+v %v", firstPreview, secondPreview, err)
	}
}

func TestThumbnailDownsamplingAveragesRatherThanAliasing(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 1024, 1024))
	for y := range 1024 {
		for x := range 1024 {
			if (x+y)%2 == 0 {
				img.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	var thumb bytes.Buffer
	_, result, err := SanitizeWithThumbnail(t.Context(), encodePNG(t, img), "image/png", io.Discard, &thumb, Limits{})
	if err != nil || result.Width != 512 || result.Height != 512 {
		t.Fatalf("wrong thumbnail dimensions: %+v %v", result, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range []image.Point{{X: 0, Y: 0}, {X: 201, Y: 107}, {X: 511, Y: 511}} {
		got := color.GrayModel.Convert(decoded.At(point.X, point.Y)).(color.Gray)
		if got.Y < 120 || got.Y > 135 {
			t.Fatalf("checkerboard alias at %v: %v", point, got)
		}
	}
}

func TestSanitizeWithThumbnailRejectsErrorsAndSharesTheDecodeGate(t *testing.T) {
	raw := encodePNG(t, sampleImage())
	var full, thumb bytes.Buffer
	if first, second, err := SanitizeWithThumbnail(t.Context(), raw, "image/png", &full, nil, Limits{}); !errors.Is(err, ErrInvalid) || first != (Result{}) || second != (Result{}) || full.Len() != 0 {
		t.Fatalf("missing thumbnail writer: %+v %+v %v", first, second, err)
	}
	if first, second, err := SanitizeWithThumbnail(t.Context(), raw[:len(raw)-1], "image/png", &full, &thumb, Limits{}); !errors.Is(err, ErrInvalid) || first != (Result{}) || second != (Result{}) || full.Len() != 0 || thumb.Len() != 0 {
		t.Fatalf("invalid frame wrote output: %+v %+v %v", first, second, err)
	}
	originalError := errors.New("original output failed")
	if first, second, err := SanitizeWithThumbnail(t.Context(), raw, "image/png", failingWriter{originalError}, &thumb, Limits{}); !errors.Is(err, originalError) || first != (Result{}) || second != (Result{}) || thumb.Len() != 0 {
		t.Fatalf("original output failure wrote thumbnail: %+v %+v %v", first, second, err)
	}
	stopped, stopOriginal := context.WithCancel(t.Context())
	defer stopOriginal()
	if first, second, err := SanitizeWithThumbnail(stopped, raw, "image/png", cancelWriter{stopOriginal}, &thumb, Limits{}); !errors.Is(err, context.Canceled) || first != (Result{}) || second != (Result{}) || thumb.Len() != 0 {
		t.Fatalf("original output cancellation wrote thumbnail: %+v %+v %v", first, second, err)
	}
	writeError := errors.New("thumbnail output failed")
	first, second, err := SanitizeWithThumbnail(t.Context(), raw, "image/png", &full, failingWriter{writeError}, Limits{})
	if !errors.Is(err, writeError) || first != (Result{}) || second != (Result{}) || full.Len() == 0 {
		t.Fatalf("thumbnail output failure: %+v %+v %v", first, second, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	if first, second, err := SanitizeWithThumbnail(cancelled, raw, "image/png", io.Discard, cancelWriter{cancel}, Limits{}); !errors.Is(err, context.Canceled) || first != (Result{}) || second != (Result{}) {
		t.Fatalf("thumbnail cancellation: %+v %+v %v", first, second, err)
	}

	paused := &pausedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(paused.release)
		}
	}()
	finished := make(chan error, 1)
	go func() {
		_, _, err := SanitizeWithThumbnail(context.Background(), raw, "image/png", io.Discard, paused, Limits{})
		finished <- err
	}()
	select {
	case <-paused.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail did not reach output")
	}
	deadline, stop := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stop()
	_, err = Sanitize(deadline, raw, "image/png", io.Discard, Limits{})
	close(paused.release)
	released = true
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("thumbnail did not retain the decode gate: %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("first decode failed: %v", err)
	}
}

type cancelSamplingImage struct {
	image.Image
	cancel context.CancelFunc
	pixels int
}

func (img *cancelSamplingImage) At(x, y int) color.Color {
	img.pixels++
	if img.pixels == 17 {
		img.cancel()
	}
	return img.Image.At(x, y)
}

func TestThumbnailSamplingRespectsCancellationAndImageOrigin(t *testing.T) {
	source := image.NewNRGBA(image.Rect(13, 23, 45, 55))
	for y := 23; y < 55; y++ {
		for x := 13; x < 45; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var output bytes.Buffer
	result, err := writeThumbnail(t.Context(), source, &output)
	if err != nil || result.Width != 32 || result.Height != 32 {
		t.Fatalf("nonzero image origin: %+v %v", result, err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got := color.RGBAModel.Convert(decoded.At(16, 16)).(color.RGBA)
	if got.R < 245 || got.G > 10 || got.B > 10 {
		t.Fatalf("sampling ignored image origin: %v", got)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopping := &cancelSamplingImage{Image: source, cancel: cancel}
	var cancelledOutput bytes.Buffer
	result, err = writeThumbnail(cancelled, stopping, &cancelledOutput)
	if !errors.Is(err, context.Canceled) || result != (Result{}) || stopping.pixels < 17 || cancelledOutput.Len() != 0 {
		t.Fatalf("sampling cancellation: %+v %d pixels %v", result, stopping.pixels, err)
	}
}

func FuzzSanitizeWithThumbnail(f *testing.F) {
	f.Add(encodePNG(f, sampleImage()), byte(0))
	f.Add(encodeJPEG(f, sampleImage()), byte(1))
	f.Add([]byte("\x89PNG\r\n\x1a\n"), byte(0))
	f.Fuzz(func(t *testing.T, raw []byte, format byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		kind := "image/png"
		if format&1 != 0 {
			kind = "image/jpeg"
		}
		var full, thumb bytes.Buffer
		original, result, err := SanitizeWithThumbnail(t.Context(), raw, kind, &full, &thumb, Limits{InputBytes: 1 << 20, Dimension: 128, Pixels: 4096})
		if err != nil {
			return
		}
		if original.SHA256 != sha256.Sum256(full.Bytes()) || original.Bytes != int64(full.Len()) || result.MIMEType != "image/jpeg" ||
			result.SHA256 != sha256.Sum256(thumb.Bytes()) || result.Bytes != int64(thumb.Len()) || result.Width > MaxThumbnailDimension || result.Height > MaxThumbnailDimension {
			t.Fatal("successful result did not describe encoded bytes")
		}
		orientation, err := inspectJPEG(thumb.Bytes())
		if err != nil || orientation != 1 {
			t.Fatalf("invalid JPEG thumbnail: %d %v", orientation, err)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(thumb.Bytes()))
		if err != nil || decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height {
			t.Fatalf("thumbnail dimensions do not match result: %v", err)
		}
	})
}
