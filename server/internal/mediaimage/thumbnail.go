package mediaimage

import (
	"context"
	"crypto/sha256"
	"image"
	"image/color"
	"image/jpeg"
	"io"
)

// MaxThumbnailDimension bounds both dimensions; smaller sources are not enlarged.
const MaxThumbnailDimension = 512

// writeThumbnail averages source pixels in bounded boxes. JPEG has no alpha, so
// transparent pixels are composited onto white before averaging and encoding.
func writeThumbnail(ctx context.Context, source image.Image, dst io.Writer) (Result, error) {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return Result{}, ErrInvalid
	}
	targetWidth, targetHeight := width, height
	if width > MaxThumbnailDimension || height > MaxThumbnailDimension {
		if width >= height {
			targetWidth = MaxThumbnailDimension
			targetHeight = max(1, (height*MaxThumbnailDimension+width/2)/width)
		} else {
			targetHeight = MaxThumbnailDimension
			targetWidth = max(1, (width*MaxThumbnailDimension+height/2)/height)
		}
	}
	thumbnail := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := range targetHeight {
		y0, y1 := y*height/targetHeight, (y+1)*height/targetHeight
		for x := range targetWidth {
			if x%16 == 0 {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
			}
			x0, x1 := x*width/targetWidth, (x+1)*width/targetWidth
			var red, green, blue uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, a := source.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
					white := uint64(0xffff - a)
					red += uint64(r) + white
					green += uint64(g) + white
					blue += uint64(b) + white
				}
			}
			count := uint64((x1 - x0) * (y1 - y0))
			channel := func(sum uint64) uint8 { return uint8(((sum+count/2)/count + 128) / 257) }
			thumbnail.SetRGBA(x, y, color.RGBA{R: channel(red), G: channel(green), B: channel(blue), A: 255})
		}
	}
	hash := sha256.New()
	out := &countingWriter{writer: io.MultiWriter(contextWriter{ctx: ctx, writer: dst}, hash)}
	if err := jpeg.Encode(out, thumbnail, &jpeg.Options{Quality: 85}); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result := Result{MIMEType: "image/jpeg", Width: targetWidth, Height: targetHeight, Bytes: out.count}
	copy(result.SHA256[:], hash.Sum(nil))
	return result, nil
}
