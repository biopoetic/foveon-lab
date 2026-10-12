package render

import (
	"bytes"
	"encoding/binary"
	"image"
	"sort"
)

// DecodeImage is image.Decode that also accepts TIFFs whose tags are out of
// order. Sigma Photo Pro writes such files (its EXIF/ICC tags come last),
// which golang.org/x/image/tiff rejects; the first IFD's entries are sorted
// in a copy of the data and decoding is retried.
func DecodeImage(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err == nil {
		return img, format, nil
	}
	if fixed, ok := sortTIFFTags(data); ok {
		if img, f2, err2 := image.Decode(bytes.NewReader(fixed)); err2 == nil {
			return img, f2, nil
		}
	}
	return nil, format, err
}

func sortTIFFTags(data []byte) ([]byte, bool) {
	if len(data) < 8 {
		return nil, false
	}
	var bo binary.ByteOrder
	switch string(data[:4]) {
	case "II*\x00":
		bo = binary.LittleEndian
	case "MM\x00*":
		bo = binary.BigEndian
	default:
		return nil, false
	}
	off := int(bo.Uint32(data[4:]))
	if off+2 > len(data) {
		return nil, false
	}
	n := int(bo.Uint16(data[off:]))
	start, end := off+2, off+2+12*n
	if end > len(data) {
		return nil, false
	}
	out := append([]byte(nil), data...)
	entries := make([][]byte, n)
	for i := range entries {
		entries[i] = out[start+12*i : start+12*i+12 : start+12*i+12]
		entries[i] = append([]byte(nil), entries[i]...)
	}
	sort.SliceStable(entries, func(a, b int) bool { return bo.Uint16(entries[a]) < bo.Uint16(entries[b]) })
	for i, e := range entries {
		copy(out[start+12*i:], e)
	}
	return out, true
}
