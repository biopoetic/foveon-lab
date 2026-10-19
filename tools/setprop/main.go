// setprop writes a copy of an X3F with one PROP property changed, for
// black-box tests of what SPP does with a property (e.g. the colour space
// tag: COLORSPACE sRGB vs AdobeRGB on the same raw data).
//
//	go run ./tools/setprop <in.X3F> <out.X3F> <NAME> <value>
//
// The new value is appended to the PROP string pool and the entry pointed
// at it; every section after the insertion, and the directory, move by the
// added bytes. The raw data is untouched.
package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"unicode/utf16"

	"github.com/biopoetic/foveon-lab/x3f"
)

var le = binary.LittleEndian

func main() {
	if len(os.Args) != 5 {
		log.Fatal("usage: setprop <in.X3F> <out.X3F> <NAME> <value>")
	}
	in, out, name, value := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	b, err := os.ReadFile(in)
	if err != nil {
		log.Fatal(err)
	}
	dirOff := int(le.Uint32(b[len(b)-4:]))
	if string(b[dirOff:dirOff+4]) != "SECd" {
		log.Fatal("no section directory")
	}
	n := int(le.Uint32(b[dirOff+8:]))
	prop := -1
	for i := 0; i < n; i++ {
		e := dirOff + 12 + 12*i
		if string(b[e+8:e+12]) == "PROP" {
			prop = e
		}
	}
	if prop < 0 {
		log.Fatal("no PROP section")
	}
	ps := int(le.Uint32(b[prop:]))
	if string(b[ps:ps+4]) != "SECp" || le.Uint32(b[ps+12:]) != 0 {
		log.Fatal("PROP is not a UTF-16 SECp section")
	}
	ne := int(le.Uint32(b[ps+8:]))
	pool := ps + 24 + 8*ne
	poolChars := int(le.Uint32(b[ps+20:]))
	str := func(off int) string {
		var u []uint16
		for i := pool + 2*off; ; i += 2 {
			c := le.Uint16(b[i:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return string(utf16.Decode(u))
	}
	entry := -1
	for i := 0; i < ne; i++ {
		if str(int(le.Uint32(b[ps+24+8*i:]))) == name {
			entry = ps + 24 + 8*i
		}
	}
	if entry < 0 {
		log.Fatalf("no property %s", name)
	}
	old := str(int(le.Uint32(b[entry+4:])))
	// The new string, NUL-terminated, padded to a multiple of 4 bytes.
	u := append(utf16.Encode([]rune(value)), 0)
	for len(u)%2 != 0 {
		u = append(u, 0)
	}
	add := make([]byte, 2*len(u))
	for i, c := range u {
		le.PutUint16(add[2*i:], c)
	}
	at := pool + 2*poolChars // insertion point: the end of the pool
	delta := len(add)
	nb := make([]byte, 0, len(b)+delta)
	nb = append(nb, b[:at]...)
	nb = append(nb, add...)
	nb = append(nb, b[at:]...)
	le.PutUint32(nb[entry+4:], uint32(poolChars))      // the entry -> new value
	le.PutUint32(nb[ps+20:], uint32(poolChars+len(u))) // pool length (chars)
	nd := dirOff + delta
	for i := 0; i < n; i++ {
		e := nd + 12 + 12*i
		off := int(le.Uint32(nb[e:]))
		switch {
		case off == ps:
			le.PutUint32(nb[e+4:], le.Uint32(nb[e+4:])+uint32(delta))
		case off >= at:
			le.PutUint32(nb[e:], uint32(off+delta))
		}
	}
	le.PutUint32(nb[len(nb)-4:], uint32(nd))
	if err := os.WriteFile(out, nb, 0o644); err != nil {
		log.Fatal(err)
	}
	f, err := x3f.Open(out)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := f.DecodeRaw(); err != nil {
		log.Fatalf("the copy does not decode: %v", err)
	}
	fmt.Printf("%s: %s %q -> %q (+%d bytes); copy decodes\n", out, name, old, f.Props[name], delta)
}
