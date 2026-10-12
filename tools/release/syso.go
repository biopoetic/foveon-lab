package main

// writeSyso writes a Windows resource object (COFF .syso) holding the app
// icon, so `go build` for windows/amd64 embeds it in foveon-lab.exe (Go
// links any *_windows_amd64.syso in the package automatically). It is the
// .rsrc layout Windows expects: an RT_ICON per size and one RT_GROUP_ICON
// that lists them; the data-entry addresses are relocations the linker
// resolves.

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"

	xdraw "golang.org/x/image/draw"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langNeutral = 0x0409
)

func writeSyso(path string) error {
	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	big := drawIcon(256)
	var icons [][]byte
	for _, sz := range sizes {
		img := image.Image(big)
		if sz != 256 {
			small := image.NewRGBA(image.Rect(0, 0, sz, sz))
			xdraw.CatmullRom.Scale(small, small.Bounds(), big, big.Bounds(), xdraw.Src, nil)
			img = small
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			return err
		}
		icons = append(icons, b.Bytes())
	}
	// RT_GROUP_ICON payload: GRPICONDIR + GRPICONDIRENTRY per icon.
	var grp bytes.Buffer
	binary.Write(&grp, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))})
	for i, sz := range sizes {
		dim := byte(sz)
		if sz >= 256 {
			dim = 0
		}
		grp.Write([]byte{dim, dim, 0, 0})
		binary.Write(&grp, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&grp, binary.LittleEndian, uint32(len(icons[i])))
		binary.Write(&grp, binary.LittleEndian, uint16(i+1))
	}

	type leaf struct {
		typ, id uint32
		data    []byte
	}
	var leaves []leaf
	for i, b := range icons {
		leaves = append(leaves, leaf{rtIcon, uint32(i + 1), b})
	}
	leaves = append(leaves, leaf{rtGroupIcon, 1, grp.Bytes()})

	// Layout: root dir (2 types) | type dirs | name dirs → lang dirs | data
	// entries | data. Directory = 16 bytes + 8 per entry.
	dirSize := func(n int) int { return 16 + 8*n }
	nIcons := len(icons)
	off := 0
	rootOff := off
	off += dirSize(2)
	iconTypeOff := off
	off += dirSize(nIcons)
	grpTypeOff := off
	off += dirSize(1)
	langOff := make([]int, len(leaves))
	for i := range leaves {
		langOff[i] = off
		off += dirSize(1)
	}
	entryOff := make([]int, len(leaves))
	for i := range leaves {
		entryOff[i] = off
		off += 16
	}
	dataOff := make([]int, len(leaves))
	for i, l := range leaves {
		off = (off + 7) &^ 7
		dataOff[i] = off
		off += len(l.data)
	}
	sec := make([]byte, (off+7)&^7)
	le := binary.LittleEndian
	putDir := func(at int, entries [][2]uint32) {
		le.PutUint16(sec[at+14:], uint16(len(entries))) // NumberOfIdEntries
		for i, e := range entries {
			le.PutUint32(sec[at+16+8*i:], e[0])
			le.PutUint32(sec[at+16+8*i+4:], e[1])
		}
	}
	const sub = 0x80000000
	putDir(rootOff, [][2]uint32{{rtIcon, sub | uint32(iconTypeOff)}, {rtGroupIcon, sub | uint32(grpTypeOff)}})
	var iconEntries [][2]uint32
	for i := 0; i < nIcons; i++ {
		iconEntries = append(iconEntries, [2]uint32{leaves[i].id, sub | uint32(langOff[i])})
	}
	putDir(iconTypeOff, iconEntries)
	putDir(grpTypeOff, [][2]uint32{{1, sub | uint32(langOff[nIcons])}})
	var relocs []uint32 // section offsets of fields holding data addresses
	for i, l := range leaves {
		putDir(langOff[i], [][2]uint32{{langNeutral, uint32(entryOff[i])}})
		le.PutUint32(sec[entryOff[i]:], uint32(dataOff[i])) // RVA, relocated by the linker
		le.PutUint32(sec[entryOff[i]+4:], uint32(len(l.data)))
		relocs = append(relocs, uint32(entryOff[i]))
		copy(sec[dataOff[i]:], l.data)
	}

	// COFF object: header, one section header, raw data, relocations,
	// symbol table (the section symbol) and an empty string table.
	var out bytes.Buffer
	const hdr, shdr = 20, 40
	rawPtr := hdr + shdr
	relPtr := rawPtr + len(sec)
	symPtr := relPtr + 10*len(relocs)
	binary.Write(&out, le, uint16(0x8664)) // AMD64
	binary.Write(&out, le, uint16(1))      // sections
	binary.Write(&out, le, uint32(0))      // timestamp
	binary.Write(&out, le, uint32(symPtr))
	binary.Write(&out, le, uint32(1)) // symbols
	binary.Write(&out, le, uint16(0)) // optional header size
	binary.Write(&out, le, uint16(0)) // characteristics
	out.WriteString(".rsrc\x00\x00\x00")
	binary.Write(&out, le, uint32(0)) // virtual size
	binary.Write(&out, le, uint32(0)) // virtual address
	binary.Write(&out, le, uint32(len(sec)))
	binary.Write(&out, le, uint32(rawPtr))
	binary.Write(&out, le, uint32(relPtr))
	binary.Write(&out, le, uint32(0)) // line numbers
	binary.Write(&out, le, uint16(len(relocs)))
	binary.Write(&out, le, uint16(0))
	binary.Write(&out, le, uint32(0x40000040)) // initialized data, readable
	out.Write(sec)
	for _, r := range relocs {
		binary.Write(&out, le, r)         // offset in section
		binary.Write(&out, le, uint32(0)) // symbol 0: the section
		binary.Write(&out, le, uint16(3)) // IMAGE_REL_AMD64_ADDR32NB
	}
	out.WriteString(".rsrc\x00\x00\x00")
	binary.Write(&out, le, uint32(0)) // value
	binary.Write(&out, le, uint16(1)) // section number
	binary.Write(&out, le, uint16(0)) // type
	out.WriteByte(3)                  // storage class: static
	out.WriteByte(0)                  // aux symbols
	binary.Write(&out, le, uint32(4)) // empty string table
	return os.WriteFile(path, out.Bytes(), 0o644)
}
