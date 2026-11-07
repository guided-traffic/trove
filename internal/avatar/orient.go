package avatar

import (
	"encoding/binary"
	"image"
)

// A camera stores a photo as the sensor saw it and says in the EXIF orientation how to turn it. The re-encoded avatar has no
// EXIF, so the turn is applied first. It is applied to the scaled square, not to the source: the centre square of a turned
// image is the turned centre square of the stored one, and the square is OutputSide pixels, not millions.

// exifOrientation reads the orientation (1 to 8) from the first Exif segment of a JPEG, 1 when there is none or it is not
// well formed. It reads only inside data and gives up after the segments the header of a JPEG holds before the image.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	off := 2
	for off+4 <= len(data) {
		if data[off] != 0xFF {
			return 1
		}
		marker := data[off+1]
		if marker == 0xFF { // fill byte
			off++
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // image data or end of image: no Exif after this
			return 1
		}
		n := int(binary.BigEndian.Uint16(data[off+2:]))
		if n < 2 || off+2+n > len(data) {
			return 1
		}
		seg := data[off+4 : off+2+n]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		off += 2 + n
	}
	return 1
}

// tiffOrientation reads tag 0x0112 from the first directory of a TIFF structure.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(t[2:]) != 42 {
		return 1
	}
	ifd := int(order.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	count := int(order.Uint16(t[ifd:]))
	for i := range count {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			return 1
		}
		const tagOrientation, typeShort = 0x0112, 3
		if order.Uint16(t[e:]) != tagOrientation {
			continue
		}
		if order.Uint16(t[e+2:]) != typeShort || order.Uint32(t[e+4:]) != 1 {
			return 1
		}
		if v := int(order.Uint16(t[e+8:])); v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}

// orient applies an EXIF orientation to a square image.
func orient(src *image.NRGBA, o int) *image.NRGBA {
	if o <= 1 || o > 8 {
		return src
	}
	n := src.Bounds().Dx()
	dst := image.NewNRGBA(src.Bounds())
	for y := range n {
		for x := range n {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = n-1-x, y
			case 3:
				sx, sy = n-1-x, n-1-y
			case 4:
				sx, sy = x, n-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, n-1-x
			case 7:
				sx, sy = n-1-y, n-1-x
			default: // 8
				sx, sy = n-1-y, x
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}
