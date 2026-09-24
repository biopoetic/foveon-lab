// Package x3f reads the container structure of Sigma/Foveon X3F raw files:
// the section directory, the embedded JPEG previews and the PROP metadata.
// It does NOT decode the Foveon raw data itself.
package x3f

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf16"
)

// Section is one entry of the X3F section directory.
type Section struct {
	Offset uint32
	Length uint32
	Type   string // "IMA2", "IMAG", "PROP", "CAMF"
}

// Image describes an image section (raw data or an embedded preview).
type Image struct {
	Section
	ImgType uint32 // 2 = processed preview, 1/3 = raw
	Format  uint32 // 18 = JPEG, 30 = TRUE raw
	Width   uint32
	Height  uint32
}

// FormatJPEG is the image-section format code of an embedded JPEG.
const FormatJPEG = 18

// File is an opened X3F container (header, directory, metadata).
type File struct {
	Path     string
	Version  string // e.g. "2.3" (SD15), "3.0" (SD1)
	Width    uint32 // raw sensor columns from the header
	Height   uint32
	Rotation int // degrees clockwise from the header (0/90/180/270)
	Images   []Image
	Props    map[string]string
}

var le = binary.LittleEndian

// Open parses the X3F header, directory and PROP section. It reads only the
// small structural parts of the file, never the raw image data.
func Open(path string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	return parse(fh, st.Size(), path)
}

func parse(r io.ReaderAt, size int64, path string) (*File, error) {
	if size < 64 {
		return nil, errors.New("x3f: file too small")
	}
	hdr := make([]byte, 40)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "FOVb" {
		return nil, errors.New("x3f: not an X3F file (bad magic)")
	}
	f := &File{
		Path:    path,
		Version: fmt.Sprintf("%d.%d", le.Uint16(hdr[6:8]), le.Uint16(hdr[4:6])),
		Width:   le.Uint32(hdr[28:32]),
		Height:  le.Uint32(hdr[32:36]),
		Props:   map[string]string{},
	}
	switch rot := int(le.Uint32(hdr[36:40])); rot {
	case 0, 90, 180, 270:
		f.Rotation = rot
	}

	tail := make([]byte, 4)
	if _, err := r.ReadAt(tail, size-4); err != nil {
		return nil, err
	}
	dirOff := int64(le.Uint32(tail))
	if dirOff <= 0 || dirOff+12 > size {
		return nil, errors.New("x3f: bad directory offset")
	}
	dh := make([]byte, 12)
	if _, err := r.ReadAt(dh, dirOff); err != nil {
		return nil, err
	}
	if string(dh[0:4]) != "SECd" {
		return nil, errors.New("x3f: bad directory magic")
	}
	n := int64(le.Uint32(dh[8:12]))
	if n <= 0 || n > 256 || dirOff+12+n*12 > size {
		return nil, errors.New("x3f: bad directory entry count")
	}
	ents := make([]byte, n*12)
	if _, err := r.ReadAt(ents, dirOff+12); err != nil {
		return nil, err
	}
	for i := int64(0); i < n; i++ {
		e := ents[i*12:]
		s := Section{Offset: le.Uint32(e[0:4]), Length: le.Uint32(e[4:8]), Type: string(e[8:12])}
		if int64(s.Offset)+int64(s.Length) > size {
			continue
		}
		switch s.Type {
		case "IMA2", "IMAG":
			if s.Length < 28 {
				continue
			}
			ih := make([]byte, 28)
			if _, err := r.ReadAt(ih, int64(s.Offset)); err != nil {
				return nil, err
			}
			f.Images = append(f.Images, Image{
				Section: s,
				ImgType: le.Uint32(ih[8:12]),
				Format:  le.Uint32(ih[12:16]),
				Width:   le.Uint32(ih[16:20]),
				Height:  le.Uint32(ih[20:24]),
			})
		case "PROP":
			buf := make([]byte, s.Length)
			if _, err := r.ReadAt(buf, int64(s.Offset)); err != nil {
				return nil, err
			}
			f.Props = parseProps(buf)
		}
	}
	return f, nil
}

// parseProps decodes a SECp section: a list of (name, value) offsets into a
// UTF-16LE character pool of NUL-terminated strings.
func parseProps(b []byte) map[string]string {
	out := map[string]string{}
	if len(b) < 24 || string(b[0:4]) != "SECp" || le.Uint32(b[12:16]) != 0 {
		return out
	}
	n := int(le.Uint32(b[8:12]))
	poolStart := 24 + 8*n
	if n <= 0 || poolStart > len(b) {
		return out
	}
	pool := b[poolStart:]
	str := func(charOff uint32) string {
		i := int(charOff) * 2
		var u []uint16
		for ; i+1 < len(pool); i += 2 {
			c := le.Uint16(pool[i:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return string(utf16.Decode(u))
	}
	for i := 0; i < n; i++ {
		e := b[24+8*i:]
		out[str(le.Uint32(e[0:4]))] = str(le.Uint32(e[4:8]))
	}
	return out
}

// jpegImages returns the embedded JPEG sections, largest first.
func (f *File) jpegImages() []Image {
	var js []Image
	for _, im := range f.Images {
		if im.Format == FormatJPEG {
			js = append(js, im)
		}
	}
	for i := 1; i < len(js); i++ {
		for j := i; j > 0 && js[j].Width*js[j].Height > js[j-1].Width*js[j-1].Height; j-- {
			js[j], js[j-1] = js[j-1], js[j]
		}
	}
	return js
}

// PreviewJPEG returns the bytes of the largest embedded JPEG (on the SD1 and
// SD15 this is a full-size, in-camera-processed rendering of the shot).
func (f *File) PreviewJPEG() ([]byte, Image, error) {
	js := f.jpegImages()
	if len(js) == 0 {
		return nil, Image{}, errors.New("x3f: no embedded JPEG preview")
	}
	b, err := f.readJPEG(js[0])
	return b, js[0], err
}

// ThumbJPEG returns the smallest embedded JPEG (a ~221x147 thumbnail).
func (f *File) ThumbJPEG() ([]byte, error) {
	js := f.jpegImages()
	if len(js) == 0 {
		return nil, errors.New("x3f: no embedded JPEG thumbnail")
	}
	return f.readJPEG(js[len(js)-1])
}

func (f *File) readJPEG(im Image) ([]byte, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	buf := make([]byte, im.Length-28)
	if _, err := fh.ReadAt(buf, int64(im.Offset)+28); err != nil {
		return nil, err
	}
	// The JPEG normally starts right after the 28-byte section header;
	// tolerate a little padding just in case.
	i := bytes.Index(buf[:min(len(buf), 256)], []byte{0xFF, 0xD8})
	if i < 0 {
		return nil, errors.New("x3f: embedded image is not a JPEG")
	}
	return buf[i:], nil
}

// Camera returns a short camera tag ("SD1", "SD15", ...) from the metadata.
func (f *File) Camera() string {
	m := f.Props["CAMMODEL"]
	if m == "" {
		m = f.Props["CAMNAME"]
	}
	return ShortModel(m)
}

// ShortModel turns "SIGMA SD1 Merrill" / "SIGMA SD15" into "SD1" / "SD15".
func ShortModel(model string) string {
	b := []byte(model)
	for i := 0; i+1 < len(b); i++ {
		if (b[i] == 'S' || b[i] == 's') && (b[i+1] == 'D' || b[i+1] == 'd') {
			j := i + 2
			for j < len(b) && b[j] >= '0' && b[j] <= '9' {
				j++
			}
			if j > i+2 {
				return "SD" + string(b[i+2:j])
			}
		}
	}
	return ""
}
