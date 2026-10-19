// exifcs reports the colour space tags in the EXIF of an X3F's embedded
// JPEGs, and with -set writes a copy marked AdobeRGB: ColorSpace (0xA001:
// 1 = sRGB, 0xFFFF = uncalibrated, as cameras mark AdobeRGB) and the
// interoperability index (0x0001: "R98" = sRGB, "R03" = AdobeRGB). Both
// edits keep every byte length, so nothing else in the file moves. For
// black-box tests of what SPP reads the colour space from.
//
//	go run ./tools/exifcs <in.X3F>
//	go run ./tools/exifcs <in.X3F> -set <out.X3F>
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
)

func main() {
	set := len(os.Args) == 4 && os.Args[2] == "-set"
	if len(os.Args) != 2 && !set {
		log.Fatal("usage: exifcs <in.X3F> [-set <out.X3F>]")
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	n := 0
	for off := 0; ; {
		i := bytes.Index(b[off:], []byte("Exif\x00\x00"))
		if i < 0 {
			break
		}
		t := off + i + 6 // the TIFF header
		off = t
		var bo binary.ByteOrder
		switch string(b[t : t+2]) {
		case "II":
			bo = binary.LittleEndian
		case "MM":
			bo = binary.BigEndian
		default:
			continue
		}
		n++
		// tag finds a tag's 12-byte entry in the IFD at TIFF offset at.
		tag := func(at int, want uint16) (int, bool) {
			if t+at+2 > len(b) {
				return 0, false
			}
			cnt := int(bo.Uint16(b[t+at:]))
			for k := 0; k < cnt && t+at+2+12*k+12 <= len(b); k++ {
				e := t + at + 2 + 12*k
				if bo.Uint16(b[e:]) == want {
					return e, true
				}
			}
			return 0, false
		}
		e, ok := tag(int(bo.Uint32(b[t+4:])), 0x8769) // IFD0 -> Exif IFD
		if !ok {
			fmt.Printf("EXIF #%d: no Exif IFD\n", n)
			continue
		}
		exif := int(bo.Uint32(b[e+8:]))
		if cs, ok := tag(exif, 0xA001); ok {
			fmt.Printf("EXIF #%d: ColorSpace = 0x%04X\n", n, bo.Uint16(b[cs+8:]))
			if set {
				bo.PutUint16(b[cs+8:], 0xFFFF)
			}
		} else {
			fmt.Printf("EXIF #%d: no ColorSpace tag\n", n)
		}
		if io, ok := tag(exif, 0xA005); ok {
			if ix, ok := tag(int(bo.Uint32(b[io+8:])), 0x0001); ok {
				fmt.Printf("EXIF #%d: InteropIndex = %q\n", n, b[ix+8:ix+11])
				if set {
					copy(b[ix+8:ix+11], "R03")
				}
			}
		}
	}
	if n == 0 {
		fmt.Println("no EXIF found")
	}
	if set {
		if err := os.WriteFile(os.Args[3], b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", os.Args[3])
	}
}
