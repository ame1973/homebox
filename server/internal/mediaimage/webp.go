package mediaimage

import (
	"bytes"
	"encoding/binary"
)

// inspectWebP validates the complete RIFF container before the decoder can
// stop at the first image. Animated and ambiguous/malformed metadata variants
// are refused; source metadata is discarded after orientation is applied.
func inspectWebP(raw []byte) (int, error) {
	if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" ||
		uint64(binary.LittleEndian.Uint32(raw[4:8])) != uint64(len(raw)-8) {
		return 0, ErrInvalid
	}
	var flags byte
	var extended, image, alpha, exif, icc, xmp bool
	var imageKind string
	var canvasWidth, canvasHeight uint64
	orientation := 1
	for pos := 12; pos < len(raw); {
		if len(raw)-pos < 8 {
			return 0, ErrInvalid
		}
		name := string(raw[pos : pos+4])
		length := uint64(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		if length > uint64(len(raw)-pos-8) {
			return 0, ErrInvalid
		}
		start := pos + 8
		end := start + int(length)
		next := end + int(length&1)
		if next > len(raw) || next != end && raw[end] != 0 {
			return 0, ErrInvalid
		}
		data := raw[start:end]
		switch name {
		case "VP8X":
			if extended || pos != 12 || len(data) != 10 || data[0]&0xc3 != 0 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
				return 0, ErrInvalid
			}
			extended, flags = true, data[0]
			canvasWidth = uint64(data[4]) | uint64(data[5])<<8 | uint64(data[6])<<16
			canvasHeight = uint64(data[7]) | uint64(data[8])<<8 | uint64(data[9])<<16
			canvasWidth++
			canvasHeight++
			if canvasWidth*canvasHeight > 1<<32-1 {
				return 0, ErrInvalid
			}
		case "ANIM", "ANMF":
			return 0, ErrInvalid
		case "VP8 ", "VP8L":
			if image || len(data) == 0 || alpha && name != "VP8 " || extended && flags&0x10 != 0 && name == "VP8 " && !alpha {
				return 0, ErrInvalid
			}
			image, imageKind = true, name
		case "ALPH":
			if !extended || flags&0x10 == 0 || image || alpha || len(data) < 2 {
				return 0, ErrInvalid
			}
			compression, preprocessing := data[0]&0x03, (data[0]>>4)&0x03
			if data[0]&0xc0 != 0 || compression > 1 || preprocessing > 1 ||
				compression == 0 && uint64(len(data)-1) != canvasWidth*canvasHeight {
				return 0, ErrInvalid
			}
			alpha = true
		case "ICCP":
			if !extended || flags&0x20 == 0 || icc || image || alpha {
				return 0, ErrInvalid
			}
			icc = true
		case "EXIF":
			if !extended || flags&0x08 == 0 || exif {
				return 0, ErrInvalid
			}
			exif = true
			if bytes.HasPrefix(data, []byte("Exif\x00\x00")) {
				data = data[6:]
			}
			var err error
			orientation, err = exifOrientation(data)
			if err != nil {
				return 0, err
			}
		case "XMP ":
			if !extended || flags&0x04 == 0 || xmp {
				return 0, ErrInvalid
			}
			xmp = true
		default:
			if !image && !extended {
				return 0, ErrInvalid
			}
		}
		pos = next
	}
	if !image || alpha && imageKind != "VP8 " || extended &&
		(flags&0x20 != 0 != icc || flags&0x08 != 0 != exif || flags&0x04 != 0 != xmp) {
		return 0, ErrInvalid
	}
	return orientation, nil
}
