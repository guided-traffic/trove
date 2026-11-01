package nupkg

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// ent describes one entry of a hand-built archive. Everything a reader could be
// confused by can be set apart from the truth: the local header name, the
// declared sizes, the flags.
type ent struct {
	name      string
	body      []byte
	stored    bool   // method store instead of deflate
	localName string // the name of the local header when it differs
	usize     int64  // declared uncompressed size when not 0
	csize     int64  // declared compressed size when not 0
	flags     uint16
	crc       uint32 // checksum when not 0
	zip64     bool   // the central record carries its sizes and offset in a zip64 extra field
	rawData   []byte // the compressed bytes as they are, instead of body
	method    uint16 // method when not 0
	omit      bool   // a central record without any data (declared sizes only)
}

type build struct {
	ents    []ent
	comment string
	disk    uint16 // number of this disk in the end record
	zip64   bool   // write a zip64 end record and locator
}

func deflate(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func put16(b *bytes.Buffer, v uint16) { _ = binary.Write(b, binary.LittleEndian, v) }
func put32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
func put64(b *bytes.Buffer, v uint64) { _ = binary.Write(b, binary.LittleEndian, v) }

func (bd build) bytes() []byte {
	var out, cd bytes.Buffer
	for _, e := range bd.ents {
		comp := e.rawData
		method := uint16(8)
		if e.stored {
			method = 0
		}
		if e.method != 0 {
			method = e.method
		}
		if comp == nil {
			comp = e.body
			if method == 8 {
				comp = deflate(e.body)
			}
		}
		crc := e.crc
		if crc == 0 {
			crc = crc32.ChecksumIEEE(e.body)
		}
		usize, csize := int64(len(e.body)), int64(len(comp))
		if e.usize != 0 {
			usize = e.usize
		}
		if e.csize != 0 {
			csize = e.csize
		}
		local := e.localName
		if local == "" {
			local = e.name
		}
		at := int64(out.Len())
		if !e.omit {
			put32(&out, sigLocal)
			put16(&out, 20)
			put16(&out, e.flags)
			put16(&out, method)
			put32(&out, 0)
			put32(&out, crc)
			put32(&out, uint32(len(comp)))
			put32(&out, uint32(len(e.body)))
			put16(&out, uint16(len(local)))
			put16(&out, 0)
			out.WriteString(local)
			out.Write(comp)
		}

		var extra bytes.Buffer
		cs, us, off := uint32(csize), uint32(usize), uint32(at)
		if e.zip64 {
			put16(&extra, 0x0001)
			put16(&extra, 24)
			put64(&extra, uint64(usize))
			put64(&extra, uint64(csize))
			put64(&extra, uint64(at))
			cs, us, off = 0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF
		}
		put32(&cd, sigCentral)
		put16(&cd, 45)
		put16(&cd, 20)
		put16(&cd, e.flags)
		put16(&cd, method)
		put32(&cd, 0)
		put32(&cd, crc)
		put32(&cd, cs)
		put32(&cd, us)
		put16(&cd, uint16(len(e.name)))
		put16(&cd, uint16(extra.Len()))
		put16(&cd, 0)
		put16(&cd, 0)
		put16(&cd, 0)
		put32(&cd, 0)
		put32(&cd, off)
		cd.WriteString(e.name)
		cd.Write(extra.Bytes())
	}
	cdAt := int64(out.Len())
	out.Write(cd.Bytes())
	n := uint16(len(bd.ents))
	cdLen, cdOff := uint32(cd.Len()), uint32(cdAt)
	if bd.zip64 {
		rec64 := int64(out.Len())
		put32(&out, sigEOCD64)
		put64(&out, 44)
		put16(&out, 45)
		put16(&out, 45)
		put32(&out, 0)
		put32(&out, 0)
		put64(&out, uint64(len(bd.ents)))
		put64(&out, uint64(len(bd.ents)))
		put64(&out, uint64(cd.Len()))
		put64(&out, uint64(cdAt))
		put32(&out, sigLocator)
		put32(&out, 0)
		put64(&out, uint64(rec64))
		put32(&out, 1)
		n, cdLen, cdOff = 0xFFFF, 0xFFFFFFFF, 0xFFFFFFFF
	}
	put32(&out, sigEOCD)
	put16(&out, bd.disk)
	put16(&out, 0)
	put16(&out, n)
	put16(&out, n)
	put32(&out, cdLen)
	put32(&out, cdOff)
	put16(&out, uint16(len(bd.comment)))
	out.WriteString(bd.comment)
	return out.Bytes()
}

const nuspecXML = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata>
<id>Acme.Widgets</id><version>1.2.3</version><authors>Acme</authors><description>d</description>
</metadata></package>`

func nuspecEnt(name string) ent { return ent{name: name, body: []byte(nuspecXML)} }

// good is a small valid package.
func good() build {
	return build{ents: []ent{
		nuspecEnt("Acme.Widgets.nuspec"),
		{name: "lib/net8.0/Acme.Widgets.dll", body: bytes.Repeat([]byte("MZ"), 500)},
		{name: "[Content_Types].xml", body: []byte(`<Types/>`)},
		{name: "_rels/.rels", body: []byte(`<Relationships/>`)},
		{name: "lib/", body: nil, stored: true},
	}}
}

// overlapping is a package whose second entry declares more compressed bytes
// than it has, so that its data covers the headers that follow.
func overlapping() build {
	bd := good()
	bd.ents = append([]ent(nil), bd.ents...)
	bd.ents[1].csize = 100
	return bd
}

// with returns bd with the entries e appended.
func (bd build) with(e ...ent) build {
	bd.ents = append(append([]ent(nil), bd.ents...), e...)
	return bd
}

// without returns bd without the entry of that name.
func (bd build) without(name string) build {
	var out []ent
	for _, e := range bd.ents {
		if e.name != name {
			out = append(out, e)
		}
	}
	bd.ents = out
	return bd
}

func mustBytes(t testing.TB, bd build) []byte {
	t.Helper()
	return bd.bytes()
}
