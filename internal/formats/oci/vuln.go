package oci

import "github.com/guided-traffic/trove/internal/vuln/advisory"

// ImageEcosystem implements [formats.ImageScanned]: the content of an image is read by the image scanning (R-VULN-033), and nothing matches
// an advisory against an image by name and version, which is why the format has no vulnerability adapter.
func (*Format) ImageEcosystem() string { return advisory.EcosystemOCI }
