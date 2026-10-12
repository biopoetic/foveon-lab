package main

// "All images": ordinary JPEG/TIFF/PNG files next to the X3Fs. Presets are
// applied to them as a look only — they are not Foveon raw data, and SPP
// can't open them.

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/png" // registers PNG for image.Decode
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/biopoetic/foveon-lab/render"
	_ "golang.org/x/image/tiff" // registers TIFF for image.Decode
)

// lensDesc describes the lens from the X3F header's focal-length and
// aperture ranges, e.g. "18–35mm f/1.8–16" (the header's LENSMODEL is only a
// Sigma ID number). Quattro files have no such header: "".
func lensDesc(props map[string]string) string {
	rng := func(s string) []float64 {
		var out []float64
		for _, f := range strings.Fields(strings.ReplaceAll(s, ",", ".")) {
			if v, err := strconv.ParseFloat(f, 64); err == nil && v > 0 {
				out = append(out, v)
			}
		}
		sort.Float64s(out)
		return out
	}
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	var parts []string
	if fl := rng(props["LENSFRANGE"]); len(fl) > 0 {
		s := num(fl[0])
		if fl[len(fl)-1] != fl[0] {
			s += "–" + num(fl[len(fl)-1])
		}
		parts = append(parts, s+"mm")
	}
	if ap := rng(props["LENSARANGE"]); len(ap) > 0 {
		s := "f/" + num(ap[0])
		if ap[len(ap)-1] != ap[0] {
			s += "–" + num(ap[len(ap)-1])
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

var otherImageExt = map[string]bool{".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true, ".png": true}

func decodeImageFile(path string) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, _, err := render.DecodeImage(data)
	return img, err
}

// exifRotation reads a JPEG's EXIF orientation as a clockwise rotation
// (0, 90, 180 or 270). Mirrored orientations are treated as their rotation.
func exifRotation(path string) int {
	if ext := strings.ToLower(path[strings.LastIndex(path, ".")+1:]); ext != "jpg" && ext != "jpeg" {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	head := make([]byte, 128<<10) // EXIF lives in the first APP1 segment
	n, _ := io.ReadFull(f, head)
	return rotationFromJPEG(head[:n])
}

func rotationFromJPEG(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 0
		}
		marker := b[i+1]
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA || size < 2 { // start of scan: no EXIF before the image data
			return 0
		}
		end := i + 2 + size
		if marker == 0xE1 && end <= len(b) && bytes.HasPrefix(b[i+4:end], []byte("Exif\x00\x00")) {
			return orientationFromTIFF(b[i+10 : end])
		}
		i = end
	}
	return 0
}

func orientationFromTIFF(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[ifd:]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			switch bo.Uint16(t[e+8:]) {
			case 3, 4:
				return 180
			case 5, 6:
				return 90
			case 7, 8:
				return 270
			}
			return 0
		}
	}
	return 0
}
