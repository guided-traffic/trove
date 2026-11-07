package format

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MarkerFormat is the format number of the target marker.
const MarkerFormat = 1

// ErrForeignTarget is the verdict of [Marker.Check]: the marker names another installation or incarnation. A create or
// delete run that meets it exits 4 (R-BAK-002, R-BAK-013).
var ErrForeignTarget = errors.New("the backup target belongs to another installation or incarnation")

// Marker is trove-backup-target.json (R-BAK-002). The first run on an empty prefix writes it; every create and delete run
// reads it at start and again before it writes a manifest or deletes anything.
type Marker struct {
	Format         int        `json:"format"`
	InstallationID string     `json:"installationId"`
	Incarnation    string     `json:"incarnation"`
	CreatedAt      time.Time  `json:"createdAt"`
	AdoptedAt      *time.Time `json:"adoptedAt"`
}

// NewMarker is the marker a first run writes for the source installation.
func NewMarker(installationID, incarnation string, now time.Time) Marker {
	return Marker{Format: MarkerFormat, InstallationID: installationID, Incarnation: incarnation, CreatedAt: now.UTC()}
}

// Encode is the JSON form, newline-terminated.
func (m Marker) Encode() ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// DecodeMarker parses a marker. A higher format than this binary knows is an error that names the format.
func DecodeMarker(b []byte) (Marker, error) {
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{}, fmt.Errorf("target marker: %w", err)
	}
	if m.Format > MarkerFormat {
		return Marker{}, &UnknownFormatError{What: "target marker", Got: m.Format, Known: MarkerFormat}
	}
	if m.InstallationID == "" || m.Incarnation == "" {
		return Marker{}, errors.New("target marker: installationId and incarnation are required")
	}
	return m, nil
}

// Check reports ErrForeignTarget, wrapped with both IDs, unless the marker names the source's installation ID and
// incarnation.
func (m Marker) Check(installationID, incarnation string) error {
	if m.InstallationID == installationID && m.Incarnation == incarnation {
		return nil
	}
	return fmt.Errorf("%w: the marker names installation %s, incarnation %s; this installation is %s, incarnation %s",
		ErrForeignTarget, m.InstallationID, m.Incarnation, installationID, incarnation)
}

// CanAdopt is the compare-and-swap of an adoption (R-BAK-002): the marker names the installation ID and incarnation that
// the source was restored from. A marker of another lineage, or one that another restored installation already adopted,
// cannot be adopted.
func (m Marker) CanAdopt(restoredFromInstallationID, restoredFromIncarnation string) bool {
	return restoredFromInstallationID != "" && m.InstallationID == restoredFromInstallationID && m.Incarnation == restoredFromIncarnation
}

// Adopt returns the marker rewritten for the new incarnation, with adoptedAt set.
func (m Marker) Adopt(installationID, incarnation string, now time.Time) Marker {
	t := now.UTC()
	m.InstallationID, m.Incarnation, m.AdoptedAt = installationID, incarnation, &t
	return m
}

// UnknownFormatError is a manifest or marker of a higher format than this binary knows. A reader refuses it and exits 6
// (R-BAK-003).
type UnknownFormatError struct {
	What       string
	Got, Known int
}

func (e *UnknownFormatError) Error() string {
	return fmt.Sprintf("%s has format %d; this trove understands formats up to %d", e.What, e.Got, e.Known)
}
