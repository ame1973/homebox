package mediaimage

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// inspectJPEG walks every segment and entropy-coded scan rather than trusting
// DecodeConfig to inspect metadata after the first frame header. It requires
// one complete image and rejects appended files after its EOI marker.
func inspectJPEG(raw []byte) (int, error) {
	if len(raw) < 4 || raw[0] != 0xff || raw[1] != 0xd8 {
		return 0, ErrInvalid
	}
	orientation, seenEXIF, seenScan := 1, false, false
	pos, scanning := 2, false
	for pos < len(raw) {
		if scanning {
			for {
				next := bytes.IndexByte(raw[pos:], 0xff)
				if next < 0 {
					return 0, ErrInvalid
				}
				start := pos + next
				pos = start
				for pos < len(raw) && raw[pos] == 0xff {
					pos++
				}
				if pos == len(raw) {
					return 0, ErrInvalid
				}
				marker := raw[pos]
				pos++
				if marker == 0x00 {
					if pos-start != 2 {
						return 0, ErrInvalid
					}
					continue
				}
				if marker >= 0xd0 && marker <= 0xd7 {
					continue
				}
				pos, scanning = start, false
				break
			}
		}
		if raw[pos] != 0xff {
			return 0, ErrInvalid
		}
		for pos < len(raw) && raw[pos] == 0xff {
			pos++
		}
		if pos == len(raw) {
			return 0, ErrInvalid
		}
		marker := raw[pos]
		pos++
		if marker == 0xd9 {
			if !seenScan || pos != len(raw) {
				return 0, ErrInvalid
			}
			return orientation, nil
		}
		if marker == 0 || marker == 0x01 || marker == 0xd8 || marker >= 0xd0 && marker <= 0xd7 || pos+2 > len(raw) {
			return 0, ErrInvalid
		}
		length := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
		if length < 2 || length > len(raw)-pos {
			return 0, ErrInvalid
		}
		segment := raw[pos+2 : pos+length]
		pos += length
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			if seenEXIF {
				return 0, ErrInvalid
			}
			seenEXIF = true
			var err error
			orientation, err = exifOrientation(segment[6:])
			if err != nil {
				return 0, err
			}
		}
		if marker == 0xda {
			seenScan, scanning = true, true
		}
	}
	return 0, ErrInvalid
}

// inspectPNG verifies all chunk boundaries and checksums, rejects APNG even
// after IDAT, and reads orientation before the source metadata is discarded.
func inspectPNG(raw []byte) (int, error) {
	if !bytes.HasPrefix(raw, pngSignature) {
		return 0, ErrInvalid
	}
	orientation, seenEXIF := 1, false
	for pos := len(pngSignature); pos < len(raw); {
		if len(raw)-pos < 12 {
			return 0, ErrInvalid
		}
		length := uint64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		if length > uint64(len(raw)-pos-12) {
			return 0, ErrInvalid
		}
		end := pos + 8 + int(length)
		name := raw[pos+4 : pos+8]
		for _, c := range name {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
				return 0, ErrInvalid
			}
		}
		if name[2] < 'A' || name[2] > 'Z' || crc32.ChecksumIEEE(raw[pos+4:end]) != binary.BigEndian.Uint32(raw[end:end+4]) {
			return 0, ErrInvalid
		}
		switch string(name) {
		case "acTL", "fcTL", "fdAT":
			return 0, ErrInvalid
		case "eXIf":
			if seenEXIF {
				return 0, ErrInvalid
			}
			seenEXIF = true
			var err error
			orientation, err = exifOrientation(raw[pos+8 : end])
			if err != nil {
				return 0, err
			}
		case "IEND":
			if length != 0 || end+4 != len(raw) {
				return 0, ErrInvalid
			}
			return orientation, nil
		case "IHDR", "PLTE", "IDAT":
		default:
			if name[0] >= 'A' && name[0] <= 'Z' {
				return 0, ErrInvalid
			}
		}
		pos = end + 4
	}
	return 0, ErrInvalid
}

// exifOrientation reads only IFD0's inline TIFF Orientation. All other EXIF,
// including GPS pointers and thumbnail bytes, is discarded with the input.
func exifOrientation(tiff []byte) (int, error) {
	if len(tiff) < 8 {
		return 0, ErrInvalid
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, ErrInvalid
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 0, ErrInvalid
	}
	offset := uint64(order.Uint32(tiff[4:8]))
	if offset < 8 || offset+2 > uint64(len(tiff)) {
		return 0, ErrInvalid
	}
	count := uint64(order.Uint16(tiff[int(offset) : int(offset)+2]))
	start := offset + 2
	if start+count*12+4 > uint64(len(tiff)) {
		return 0, ErrInvalid
	}
	orientation, found := 1, false
	for i := uint64(0); i < count; i++ {
		pos := int(start + i*12)
		entry := tiff[pos : pos+12]
		if order.Uint16(entry[:2]) != 0x0112 {
			continue
		}
		if found || order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0, ErrInvalid
		}
		found = true
		orientation = int(order.Uint16(entry[8:10]))
		if orientation < 1 || orientation > 8 {
			return 0, ErrInvalid
		}
	}
	return orientation, nil
}
