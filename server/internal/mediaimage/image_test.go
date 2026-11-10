package mediaimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"
)

var privateGPSMarker = []byte("GPS-PRIVATE-51.5072,-0.1276")

// testEXIF contains both an actual GPS IFD pointer and private marker bytes.
func testEXIF(order binary.ByteOrder, orientation uint16) []byte {
	data := make([]byte, 38+30+24+len(privateGPSMarker))
	if order == binary.LittleEndian {
		copy(data, "II")
	} else {
		copy(data, "MM")
	}
	order.PutUint16(data[2:4], 42)
	order.PutUint32(data[4:8], 8)
	order.PutUint16(data[8:10], 2)
	order.PutUint16(data[10:12], 0x0112)
	order.PutUint16(data[12:14], 3)
	order.PutUint32(data[14:18], 1)
	order.PutUint16(data[18:20], orientation)
	order.PutUint16(data[22:24], 0x8825)
	order.PutUint16(data[24:26], 4)
	order.PutUint32(data[26:30], 1)
	order.PutUint32(data[30:34], 38)
	order.PutUint16(data[38:40], 2)
	order.PutUint16(data[40:42], 1)
	order.PutUint16(data[42:44], 2)
	order.PutUint32(data[44:48], 2)
	copy(data[48:52], "N\x00")
	order.PutUint16(data[52:54], 2)
	order.PutUint16(data[54:56], 5)
	order.PutUint32(data[56:60], 3)
	order.PutUint32(data[60:64], 68)
	for i, value := range []uint32{51, 1, 30, 1, 2592, 100} {
		order.PutUint32(data[68+4*i:72+4*i], value)
	}
	copy(data[92:], privateGPSMarker)
	return data
}

func sampleImage() *image.NRGBA64 {
	img := image.NewNRGBA64(image.Rect(0, 0, 2, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			value := uint16(4000 * (1 + y*2 + x))
			img.SetNRGBA64(x, y, color.NRGBA64{R: value, G: value + 71, B: value + 37, A: 60000})
		}
	}
	return img
}

func encodePNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeJPEG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngChunk(name string, data []byte) []byte {
	chunk := make([]byte, len(data)+12)
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(data)))
	copy(chunk[4:8], name)
	copy(chunk[8:], data)
	binary.BigEndian.PutUint32(chunk[8+len(data):], crc32.ChecksumIEEE(chunk[4:8+len(data)]))
	return chunk
}

func insertPNGChunk(raw []byte, name string, data []byte, beforeIEND bool) []byte {
	pos := 33
	if beforeIEND {
		pos = len(raw) - 12
	}
	result := append([]byte{}, raw[:pos]...)
	result = append(result, pngChunk(name, data)...)
	return append(result, raw[pos:]...)
}

func insertJPEGEXIF(raw []byte, data []byte) []byte {
	segment := append([]byte("Exif\x00\x00"), data...)
	result := append([]byte{}, raw[:2]...)
	result = append(result, 0xff, 0xe1, byte((len(segment)+2)>>8), byte(len(segment)+2))
	result = append(result, segment...)
	return append(result, raw[2:]...)
}

func sanitize(t testing.TB, raw []byte, kind string, limits Limits) (Result, []byte) {
	t.Helper()
	var output bytes.Buffer
	result, err := Sanitize(t.Context(), raw, kind, &output, limits)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if result.Bytes != int64(output.Len()) || result.SHA256 != sha256.Sum256(output.Bytes()) {
		t.Fatal("result did not describe actual encoded bytes")
	}
	if bytes.Contains(output.Bytes(), privateGPSMarker) || bytes.Contains(output.Bytes(), []byte("Exif\x00\x00")) {
		t.Fatal("source EXIF or private GPS bytes survived re-encoding")
	}
	switch result.MIMEType {
	case "image/png":
		data := output.Bytes()
		for pos := len(pngSignature); pos < len(data); {
			length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
			name := string(data[pos+4 : pos+8])
			switch name {
			case "IHDR", "PLTE", "tRNS", "IDAT", "IEND":
			default:
				t.Fatalf("unexpected source metadata chunk in output: %s", name)
			}
			pos += 12 + length
		}
	case "image/jpeg":
		data := output.Bytes()
		for pos := 2; pos < len(data); {
			if data[pos] != 0xff || pos+4 > len(data) {
				t.Fatal("invalid JPEG encoder segment")
			}
			marker := data[pos+1]
			if marker == 0xda {
				break
			}
			if marker >= 0xe0 && marker <= 0xef || marker == 0xfe {
				t.Fatalf("unexpected source metadata segment in output: %x", marker)
			}
			pos += 2 + int(binary.BigEndian.Uint16(data[pos+2:pos+4]))
		}
	}
	return result, output.Bytes()
}

func TestPNGOrientationPreservesPixelsAndStripsGPS(t *testing.T) {
	original := sampleImage()
	encoded := encodePNG(t, original)
	cases := []struct {
		orientation uint16
		rows        []string
	}{
		{1, []string{"ab", "cd", "ef"}},
		{2, []string{"ba", "dc", "fe"}},
		{3, []string{"fe", "dc", "ba"}},
		{4, []string{"ef", "cd", "ab"}},
		{5, []string{"ace", "bdf"}},
		{6, []string{"eca", "fdb"}},
		{7, []string{"fdb", "eca"}},
		{8, []string{"bdf", "ace"}},
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, tc := range cases {
			raw := insertPNGChunk(encoded, "eXIf", testEXIF(order, tc.orientation), false)
			raw = insertPNGChunk(raw, "tEXt", append([]byte("Location\x00"), privateGPSMarker...), true)
			result, output := sanitize(t, raw, "IMAGE/PNG", Limits{})
			decoded, err := png.Decode(bytes.NewReader(output))
			if err != nil {
				t.Fatal(err)
			}
			if result.MIMEType != "image/png" || result.Width != len(tc.rows[0]) || result.Height != len(tc.rows) || decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height {
				t.Fatalf("orientation %d produced wrong dimensions: %+v", tc.orientation, result)
			}
			for y, row := range tc.rows {
				for x, label := range row {
					index := int(label - 'a')
					want := original.NRGBA64At(index%2, index/2)
					got := color.NRGBA64Model.Convert(decoded.At(x, y)).(color.NRGBA64)
					if got != want {
						t.Fatalf("orientation %d (%d,%d): got %v, want %v", tc.orientation, x, y, got, want)
					}
				}
			}
			if _, err := inspectPNG(output); err != nil {
				t.Fatalf("output is not a complete standalone PNG: %v", err)
			}
		}
	}
}

func TestJPEGRotatesAndReencodesWithoutGPS(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := range 20 {
		for x := range 40 {
			if x < 20 {
				img.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	raw := insertJPEGEXIF(encodeJPEG(t, img), testEXIF(binary.LittleEndian, 6))
	result, output := sanitize(t, raw, "image/jpeg", Limits{})
	if result.MIMEType != "image/jpeg" || result.Width != 20 || result.Height != 40 {
		t.Fatalf("wrong JPEG result: %+v", result)
	}
	rotated, err := jpeg.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	top := color.NRGBAModel.Convert(rotated.At(10, 5)).(color.NRGBA)
	bottom := color.NRGBAModel.Convert(rotated.At(10, 35)).(color.NRGBA)
	if top.R < 200 || top.B > 40 || bottom.B < 200 || bottom.R > 40 {
		t.Fatalf("JPEG pixels did not rotate: top %v, bottom %v", top, bottom)
	}
	orientation, err := inspectJPEG(output)
	if err != nil || orientation != 1 {
		t.Fatalf("output is not a complete metadata-free JPEG: %d %v", orientation, err)
	}
}

func TestJPEGProgressiveScansAndLateEXIF(t *testing.T) {
	// A self-generated 3x2 progressive JPEG, encoded once with Pillow.
	const fixture = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAMCAgICAgMCAgIDAwMDBAYEBAQEBAgGBgUGCQgKCgkICQkKDA8MCgsOCwkJDRENDg8QEBEQCgwSExIQEw8QEBD/2wBDAQMDAwQDBAgEBAgQCwkLEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBD/wgARCAACAAMDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAT/xAAVAQEBAAAAAAAAAAAAAAAAAAAGB//aAAwDAQACEAMQAAABjFyXf//EABYQAQEBAAAAAAAAAAAAAAAAAAQFAf/aAAgBAQABBQK+IWV//8QAGhEAAAcAAAAAAAAAAAAAAAAAAAEDBDRysf/aAAgBAwEBPwFhFSqWD//EABsRAAAHAQAAAAAAAAAAAAAAAAABAwQGNXOy/9oACAECAQE/AY3StMk+SH//xAAbEAEAAQUBAAAAAAAAAAAAAAABBAACAxEjUf/aAAgBAQAGPwKSETDouA5nlf/EABcQAQEBAQAAAAAAAAAAAAAAAAExAFH/2gAIAQEAAT8hErEIAAJzf//aAAwDAQACAAMAAAAQ/wD/xAAWEQADAAAAAAAAAAAAAAAAAAAAUfD/2gAIAQMBAT8Qkof/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAECAQE/EFP/xAAWEAEBAQAAAAAAAAAAAAAAAAABMQD/2gAIAQEAAT8QEyBRDYIAAEADf//Z"
	raw, err := base64.StdEncoding.DecodeString(fixture)
	if err != nil {
		t.Fatal(err)
	}
	first := bytes.Index(raw, []byte{0xff, 0xda})
	second := first + 2 + bytes.Index(raw[first+2:], []byte{0xff, 0xda})
	if first < 0 || second <= first+2 {
		t.Fatal("fixture must have multiple scans")
	}
	segment := insertJPEGEXIF([]byte{0xff, 0xd8}, testEXIF(binary.LittleEndian, 6))[2:]
	withLateEXIF := append([]byte{}, raw[:second]...)
	withLateEXIF = append(withLateEXIF, segment...)
	withLateEXIF = append(withLateEXIF, raw[second:]...)
	result, output := sanitize(t, withLateEXIF, "image/jpeg", Limits{})
	if result.Width != 2 || result.Height != 3 {
		t.Fatalf("late EXIF was not applied: %+v", result)
	}
	if _, err := jpeg.Decode(bytes.NewReader(output)); err != nil {
		t.Fatalf("progressive result cannot be decoded: %v", err)
	}
}

func TestSanitizedDigestDoesNotIdentifyTheRawRequest(t *testing.T) {
	pngRaw := encodePNG(t, sampleImage())
	firstEXIF := testEXIF(binary.LittleEndian, 1)
	secondEXIF := append([]byte{}, firstEXIF...)
	secondEXIF[len(secondEXIF)-1] ^= 1
	first := insertPNGChunk(pngRaw, "eXIf", firstEXIF, false)
	second := insertPNGChunk(pngRaw, "eXIf", secondEXIF, false)
	if sha256.Sum256(first) == sha256.Sum256(second) {
		t.Fatal("fixture did not change raw private metadata")
	}
	firstResult, firstOutput := sanitize(t, first, "image/png", Limits{})
	secondResult, secondOutput := sanitize(t, second, "image/png", Limits{})
	if firstResult != secondResult || !bytes.Equal(firstOutput, secondOutput) {
		t.Fatal("private metadata changed the sanitized image")
	}
}

func TestSanitizeRejectsIncompleteMisleadingAndAnimatedInputs(t *testing.T) {
	pngRaw := encodePNG(t, sampleImage())
	jpegRaw := encodeJPEG(t, sampleImage())
	for _, sample := range []struct {
		name, kind string
		raw        []byte
	}{{"png", "image/png", pngRaw}, {"jpeg", "image/jpeg", jpegRaw}} {
		for i := 0; i < len(sample.raw); i++ {
			var out bytes.Buffer
			result, err := Sanitize(t.Context(), sample.raw[:i], sample.kind, &out, Limits{})
			if err == nil || result != (Result{}) || out.Len() != 0 {
				t.Fatalf("%s truncation %d/%d was accepted or wrote output", sample.name, i, len(sample.raw))
			}
		}
	}
	badCRC := append([]byte{}, pngRaw...)
	badCRC[16] ^= 1
	unknownCritical := insertPNGChunk(pngRaw, "ABCD", []byte("x"), false)
	invalidReservedBit := insertPNGChunk(pngRaw, "texT", []byte("x"), false)
	shortExif := insertPNGChunk(pngRaw, "eXIf", []byte("II"), false)
	duplicateEXIF := insertPNGChunk(insertPNGChunk(pngRaw, "eXIf", testEXIF(binary.LittleEndian, 1), false), "eXIf", testEXIF(binary.LittleEndian, 1), true)
	duplicateJPEGEXIF := insertJPEGEXIF(insertJPEGEXIF(jpegRaw, testEXIF(binary.LittleEndian, 1)), testEXIF(binary.BigEndian, 1))
	idat := bytes.Index(pngRaw, []byte("IDAT"))
	if idat < 4 {
		t.Fatal("PNG fixture has no IDAT")
	}
	idatLength := int(binary.BigEndian.Uint32(pngRaw[idat-4 : idat]))
	badPNGData := append([]byte{}, pngRaw[:idat-4]...)
	badPNGData = append(badPNGData, pngChunk("IDAT", pngRaw[idat+4:idat+5])...)
	badPNGData = append(badPNGData, pngRaw[idat+8+idatLength:]...)
	sos := bytes.Index(jpegRaw, []byte{0xff, 0xda})
	if sos < 0 {
		t.Fatal("JPEG fixture has no SOS")
	}
	scanStart := sos + 2 + int(binary.BigEndian.Uint16(jpegRaw[sos+2:sos+4]))
	badJPEGData := append(append([]byte{}, jpegRaw[:scanStart]...), 0xff, 0xd9)
	for _, sample := range []struct {
		name, kind string
		raw        []byte
		err        error
	}{
		{"wrong declared JPEG", "image/jpeg", pngRaw, ErrInvalid},
		{"wrong declared PNG", "image/png", jpegRaw, ErrInvalid},
		{"WebP not implemented", "image/webp", []byte("RIFF....WEBP"), ErrUnsupported},
		{"unsupported type", "image/gif", pngRaw, ErrUnsupported},
		{"parameters", "image/png; name=private", pngRaw, ErrUnsupported},
		{"bad MIME", "image/png\r\nInjected: value", pngRaw, ErrUnsupported},
		{"PNG trailing bytes", "image/png", append(append([]byte{}, pngRaw...), 1), ErrInvalid},
		{"JPEG trailing image", "image/jpeg", append(append([]byte{}, jpegRaw...), jpegRaw...), ErrInvalid},
		{"PNG CRC", "image/png", badCRC, ErrInvalid},
		{"PNG incomplete compressed data with valid framing", "image/png", badPNGData, ErrInvalid},
		{"JPEG incomplete scan with EOI", "image/jpeg", badJPEGData, ErrInvalid},
		{"PNG critical chunk", "image/png", unknownCritical, ErrInvalid},
		{"PNG reserved chunk bit", "image/png", invalidReservedBit, ErrInvalid},
		{"PNG bad EXIF", "image/png", shortExif, ErrInvalid},
		{"duplicate PNG EXIF", "image/png", duplicateEXIF, ErrInvalid},
		{"duplicate JPEG EXIF", "image/jpeg", duplicateJPEGEXIF, ErrInvalid},
		{"APNG control before data", "image/png", insertPNGChunk(pngRaw, "acTL", make([]byte, 8), false), ErrInvalid},
		{"APNG control after data", "image/png", insertPNGChunk(pngRaw, "fcTL", make([]byte, 26), true), ErrInvalid},
		{"APNG frame data", "image/png", insertPNGChunk(pngRaw, "fdAT", make([]byte, 4), true), ErrInvalid},
	} {
		t.Run(sample.name, func(t *testing.T) {
			var output bytes.Buffer
			result, err := Sanitize(t.Context(), sample.raw, sample.kind, &output, Limits{})
			if !errors.Is(err, sample.err) || result != (Result{}) || output.Len() != 0 {
				t.Fatalf("got result %+v, err %v, output size %d", result, err, output.Len())
			}
			if strings.Contains(err.Error(), string(privateGPSMarker)) {
				t.Fatal("error leaked image metadata")
			}
		})
	}
}

func TestSanitizeEnforcesHardAndStricterLimitsBeforeDecode(t *testing.T) {
	raw := encodePNG(t, sampleImage())
	for _, tc := range []struct {
		name   string
		raw    []byte
		limits Limits
		err    error
	}{
		{"bytes", raw, Limits{InputBytes: len(raw) - 1}, ErrTooLarge},
		{"dimension", raw, Limits{Dimension: 2}, ErrTooLarge},
		{"pixels", raw, Limits{Pixels: 5}, ErrTooLarge},
		{"negative", raw, Limits{Pixels: -1}, ErrLimits},
		{"excess bytes", raw, Limits{InputBytes: MaxInputBytes + 1}, ErrLimits},
		{"excess dimension", raw, Limits{Dimension: MaxDimension + 1}, ErrLimits},
		{"excess pixels", raw, Limits{Pixels: MaxPixels + 1}, ErrLimits},
		{"oversize raw", make([]byte, MaxInputBytes+1), Limits{}, ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			result, err := Sanitize(t.Context(), tc.raw, "image/png", &out, tc.limits)
			if !errors.Is(err, tc.err) || result != (Result{}) || out.Len() != 0 {
				t.Fatalf("got %+v, %v, %d bytes", result, err, out.Len())
			}
		})
	}
	sanitize(t, raw, "image/png", Limits{InputBytes: len(raw), Dimension: 3, Pixels: 6})

	// The valid frame's claimed dimensions exceed each hard ceiling before its
	// compressed pixels are decoded. The IHDR checksum is repaired deliberately.
	for _, dimensions := range [][2]uint32{{MaxDimension + 1, 1}, {8001, 8000}, {0, 3}} {
		modified := append([]byte{}, raw...)
		binary.BigEndian.PutUint32(modified[16:20], dimensions[0])
		binary.BigEndian.PutUint32(modified[20:24], dimensions[1])
		binary.BigEndian.PutUint32(modified[29:33], crc32.ChecksumIEEE(modified[12:29]))
		var out bytes.Buffer
		_, err := Sanitize(t.Context(), modified, "image/png", &out, Limits{})
		if err == nil || out.Len() != 0 {
			t.Fatalf("oversized/invalid IHDR %v: %v", dimensions, err)
		}
	}
	jpegRaw := encodeJPEG(t, sampleImage())
	sof := bytes.Index(jpegRaw, []byte{0xff, 0xc0})
	if sof < 0 {
		t.Fatal("JPEG fixture has no baseline frame header")
	}
	binary.BigEndian.PutUint16(jpegRaw[sof+7:sof+9], MaxDimension+1)
	var out bytes.Buffer
	if _, err := Sanitize(t.Context(), jpegRaw, "image/jpeg", &out, Limits{}); !errors.Is(err, ErrTooLarge) || out.Len() != 0 {
		t.Fatalf("oversized JPEG frame was decoded: %v", err)
	}
}

func TestEXIFRejectsMalformedOrientationRatherThanGuessing(t *testing.T) {
	valid := testEXIF(binary.BigEndian, 6)
	cases := [][]byte{
		{}, []byte("II"), append([]byte{}, valid[:10]...),
		testEXIF(binary.BigEndian, 0), testEXIF(binary.LittleEndian, 9),
	}
	badOffset := append([]byte{}, valid...)
	binary.BigEndian.PutUint32(badOffset[4:8], 0xffffffff)
	cases = append(cases, badOffset)
	badCount := append([]byte{}, valid...)
	binary.BigEndian.PutUint16(badCount[8:10], 0xffff)
	cases = append(cases, badCount)
	badType := append([]byte{}, valid...)
	binary.BigEndian.PutUint16(badType[12:14], 4)
	cases = append(cases, badType)
	badValueCount := append([]byte{}, valid...)
	binary.BigEndian.PutUint32(badValueCount[14:18], 2)
	cases = append(cases, badValueCount)
	duplicate := append([]byte{}, valid...)
	copy(duplicate[22:34], duplicate[10:22])
	cases = append(cases, duplicate)
	for i, exif := range cases {
		if _, err := exifOrientation(exif); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed EXIF %d accepted: %v", i, err)
		}
	}
	withoutOrientation := append([]byte{}, valid...)
	binary.BigEndian.PutUint16(withoutOrientation[10:12], 0x0100)
	if got, err := exifOrientation(withoutOrientation); err != nil || got != 1 {
		t.Fatalf("EXIF without orientation: %d, %v", got, err)
	}
}

type pausedWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *pausedWriter) Write(p []byte) (int, error) {
	select {
	case <-w.entered:
	default:
		close(w.entered)
		<-w.release
	}
	return len(p), nil
}

type cancelWriter struct{ cancel context.CancelFunc }

func (w cancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSanitizeRespectsCancellationOutputFailureAndMemoryGate(t *testing.T) {
	raw := encodePNG(t, sampleImage())
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := Sanitize(cancelled, raw, "image/png", io.Discard, Limits{}); !errors.Is(err, context.Canceled) || result != (Result{}) {
		t.Fatalf("pre-cancelled request: %+v %v", result, err)
	}
	writeError := errors.New("private output failed")
	if result, err := Sanitize(t.Context(), raw, "image/png", failingWriter{writeError}, Limits{}); !errors.Is(err, writeError) || result != (Result{}) {
		t.Fatalf("failed output: %+v %v", result, err)
	}
	during, stop := context.WithCancel(t.Context())
	defer stop()
	if result, err := Sanitize(during, raw, "image/png", cancelWriter{stop}, Limits{}); !errors.Is(err, context.Canceled) || result != (Result{}) {
		t.Fatalf("cancelled output: %+v %v", result, err)
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
		_, err := Sanitize(context.Background(), raw, "image/png", paused, Limits{})
		finished <- err
	}()
	select {
	case <-paused.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first decode did not reach output")
	}
	deadline, stopWaiting := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer stopWaiting()
	result, err := Sanitize(deadline, raw, "image/png", io.Discard, Limits{})
	close(paused.release)
	released = true
	if !errors.Is(err, context.DeadlineExceeded) || result != (Result{}) {
		t.Fatalf("waiting decode was not cancelled: %+v %v", result, err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("first decode failed: %v", err)
	}
}

func FuzzEXIFOrientation(f *testing.F) {
	f.Add(testEXIF(binary.LittleEndian, 6))
	f.Add(testEXIF(binary.BigEndian, 1))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		orientation, err := exifOrientation(raw)
		if err == nil && (orientation < 1 || orientation > 8) {
			t.Fatalf("invalid successful orientation: %d", orientation)
		}
	})
}

func FuzzSanitize(f *testing.F) {
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
		var output bytes.Buffer
		result, err := Sanitize(t.Context(), raw, kind, &output, Limits{InputBytes: 1 << 20, Dimension: 128, Pixels: 4096})
		if err != nil {
			return
		}
		if result.Bytes != int64(output.Len()) || result.SHA256 != sha256.Sum256(output.Bytes()) {
			t.Fatal("success did not describe output")
		}
		switch result.MIMEType {
		case "image/jpeg":
			orientation, err := inspectJPEG(output.Bytes())
			if err != nil || orientation != 1 {
				t.Fatalf("invalid JPEG output: %d %v", orientation, err)
			}
		case "image/png":
			orientation, err := inspectPNG(output.Bytes())
			if err != nil || orientation != 1 {
				t.Fatalf("invalid PNG output: %d %v", orientation, err)
			}
		default:
			t.Fatal("unsupported output MIME")
		}
	})
}
