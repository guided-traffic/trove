package nugetstub

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/xml"
	"strings"
	"time"
)

// packageTime is the modification time of every entry of a generated archive, so equal inputs give
// equal bytes.
var packageTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

const targetFramework = "netstandard2.0"

func xmlEscape(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

// Nuspec renders the .nuspec manifest of a package version.
func Nuspec(p *Package, v Version) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">` + "\n  <metadata>\n")
	b.WriteString("    <id>" + xmlEscape(p.ID) + "</id>\n")
	b.WriteString("    <version>" + xmlEscape(v.Version) + "</version>\n")
	b.WriteString("    <authors>Contoso</authors>\n")
	b.WriteString("    <description>" + xmlEscape(p.Description) + "</description>\n")
	if len(v.Dependencies) > 0 {
		b.WriteString(`    <dependencies>` + "\n" + `      <group targetFramework="` + targetFramework + `">` + "\n")
		for _, d := range v.Dependencies {
			b.WriteString(`        <dependency id="` + xmlEscape(d.ID) + `" version="` + xmlEscape(d.Range) + `" />` + "\n")
		}
		b.WriteString("      </group>\n    </dependencies>\n")
	}
	b.WriteString("  </metadata>\n</package>\n")
	return []byte(b.String())
}

// Nupkg builds the package archive of a version: the OPC parts, the manifest and one placeholder
// file under lib/. The bytes depend on the inputs only.
func Nupkg(p *Package, v Version) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: packageTime})
		if err == nil {
			_, _ = w.Write(data)
		}
	}
	add("_rels/.rels", []byte(`<?xml version="1.0" encoding="utf-8"?>`+"\n"+
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+
		`<Relationship Type="http://schemas.microsoft.com/packaging/2010/07/manifest" Target="/`+xmlEscape(p.ID)+`.nuspec" Id="R1" />`+
		`</Relationships>`))
	add(p.ID+".nuspec", Nuspec(p, v))
	add("lib/"+targetFramework+"/"+p.ID+".dll", []byte("placeholder, not an assembly: "+p.ID+" "+v.Version+"\n"))
	add("[Content_Types].xml", []byte(`<?xml version="1.0" encoding="utf-8"?>`+"\n"+
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml" />`+
		`<Default Extension="nuspec" ContentType="application/octet" />`+
		`<Default Extension="dll" ContentType="application/octet" />`+
		`</Types>`))
	_ = zw.Close()
	return buf.Bytes()
}

// PackageHash is the base64 SHA-512 of a package, the form of a registration's packageHash.
func PackageHash(nupkg []byte) string {
	sum := sha512.Sum512(nupkg)
	return base64.StdEncoding.EncodeToString(sum[:])
}
