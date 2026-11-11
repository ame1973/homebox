// Package mediaimage validates and re-encodes uploaded image bytes.
// It does not authorize uploads or publish, store, or serve media.
package mediaimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
)

const (
	MaxInputBytes = 32 << 20
	MaxDimension  = 16384
	MaxPixels     = 64000000
)

var (
	ErrInvalid     = errors.New("invalid image")
	ErrTooLarge    = errors.New("image exceeds limits")
	ErrUnsupported = errors.New("unsupported image format")
	ErrLimits      = errors.New("invalid image limits")
)

// Limits may restrict, but never increase, the contract ceilings. Zero selects
// the corresponding ceiling. Input bytes must already be bounded while read.
type Limits struct {
	InputBytes int
	Dimension  int
	Pixels     int
}

// Result describes the actual newly encoded output, not the supplied file.
type Result struct {
	MIMEType string
	Width    int
	Height   int
	Bytes    int64
	SHA256   [sha256.Size]byte
}

// ponytail: one decoder bounds concurrent image memory; use a measured weighted
// budget if greater throughput is needed. HTTP admission must also bound staging.
var decodeSlot = make(chan struct{}, 1)

// Sanitize completely decodes a JPEG or PNG, applies EXIF orientation, and
// writes freshly encoded pixels without the source metadata. WebP is not yet
// supported. The caller must not change raw concurrently and must discard the
// entire output if an error occurs. A successful result alone does not publish
// or prove the later integrity of a stored file.
func Sanitize(ctx context.Context, raw []byte, declaredMIME string, dst io.Writer, limits Limits) (Result, error) {
	return sanitizeImage(ctx, raw, declaredMIME, dst, limits, nil, nil)
}

// SanitizeWithThumbnail also writes a bounded JPEG derived from the same fully
// validated and oriented pixels. Transparent pixels are composited onto white.
// The destinations must be distinct; discard both outputs if any error occurs.
func SanitizeWithThumbnail(ctx context.Context, raw []byte, declaredMIME string, dst, thumbnail io.Writer, limits Limits) (Result, Result, error) {
	if thumbnail == nil {
		return Result{}, Result{}, ErrInvalid
	}
	var preview Result
	original, err := sanitizeImage(ctx, raw, declaredMIME, dst, limits, thumbnail, &preview)
	if err != nil {
		return Result{}, Result{}, err
	}
	return original, preview, nil
}

func sanitizeImage(ctx context.Context, raw []byte, declaredMIME string, dst io.Writer, limits Limits, thumbnail io.Writer, preview *Result) (Result, error) {
	if ctx == nil || dst == nil {
		return Result{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var err error
	if limits, err = normalizedLimits(limits); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		return Result{}, ErrInvalid
	}
	if len(raw) > limits.InputBytes {
		return Result{}, ErrTooLarge
	}
	kind, params, err := mime.ParseMediaType(declaredMIME)
	if err != nil || len(params) != 0 {
		return Result{}, ErrUnsupported
	}
	if kind != "image/jpeg" && kind != "image/png" {
		return Result{}, ErrUnsupported
	}

	select {
	case decodeSlot <- struct{}{}:
		defer func() { <-decodeSlot }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	var orientation int
	var config image.Config
	switch kind {
	case "image/jpeg":
		orientation, err = inspectJPEG(raw)
		if err == nil {
			config, err = jpeg.DecodeConfig(bytes.NewReader(raw))
		}
	case "image/png":
		orientation, err = inspectPNG(raw)
		if err == nil {
			config, err = png.DecodeConfig(bytes.NewReader(raw))
		}
	}
	if err != nil {
		return Result{}, ErrInvalid
	}
	if config.Width <= 0 || config.Height <= 0 {
		return Result{}, ErrInvalid
	}
	if config.Width > limits.Dimension || config.Height > limits.Dimension ||
		uint64(config.Width)*uint64(config.Height) > uint64(limits.Pixels) {
		return Result{}, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	reader := contextReader{ctx: ctx, reader: bytes.NewReader(raw)}
	var decoded image.Image
	switch kind {
	case "image/jpeg":
		decoded, err = jpeg.Decode(reader)
	case "image/png":
		decoded, err = png.Decode(reader)
	}
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, ErrInvalid
	}
	if decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return Result{}, ErrInvalid
	}
	if orientation != 1 {
		decoded = orient(decoded, orientation)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	hash := sha256.New()
	out := &countingWriter{writer: io.MultiWriter(contextWriter{ctx: ctx, writer: dst}, hash)}
	switch kind {
	case "image/jpeg":
		err = jpeg.Encode(out, decoded, &jpeg.Options{Quality: 90})
	case "image/png":
		err = png.Encode(out, decoded)
	}
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result := Result{MIMEType: kind, Width: decoded.Bounds().Dx(), Height: decoded.Bounds().Dy(), Bytes: out.count}
	copy(result.SHA256[:], hash.Sum(nil))
	if thumbnail != nil {
		*preview, err = writeThumbnail(ctx, decoded, thumbnail)
		if err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

func normalizedLimits(limits Limits) (Limits, error) {
	fields := []*int{&limits.InputBytes, &limits.Dimension, &limits.Pixels}
	maxima := []int{MaxInputBytes, MaxDimension, MaxPixels}
	for i, value := range fields {
		if *value < 0 || *value > maxima[i] {
			return Limits{}, ErrLimits
		}
		if *value == 0 {
			*value = maxima[i]
		}
	}
	return limits, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

// orientedImage maps the eight TIFF orientation values without allocating a
// second full-resolution image. Its pixels retain their original precision.
type orientedImage struct {
	source      image.Image
	orientation int
	bounds      image.Rectangle
}

func orient(source image.Image, orientation int) image.Image {
	width, height := source.Bounds().Dx(), source.Bounds().Dy()
	if orientation >= 5 {
		width, height = height, width
	}
	return orientedImage{source: source, orientation: orientation, bounds: image.Rect(0, 0, width, height)}
}

func (o orientedImage) ColorModel() color.Model { return o.source.ColorModel() }
func (o orientedImage) Bounds() image.Rectangle { return o.bounds }
func (o orientedImage) At(x, y int) color.Color {
	if !image.Pt(x, y).In(o.bounds) {
		return color.RGBA{}
	}
	bounds := o.source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	switch o.orientation {
	case 2:
		x = width - 1 - x
	case 3:
		x, y = width-1-x, height-1-y
	case 4:
		y = height - 1 - y
	case 5:
		x, y = y, x
	case 6:
		x, y = y, height-1-x
	case 7:
		x, y = width-1-y, height-1-x
	case 8:
		x, y = width-1-y, x
	}
	return o.source.At(bounds.Min.X+x, bounds.Min.Y+y)
}
