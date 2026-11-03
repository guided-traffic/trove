//go:build integration

package bastioniface

import (
	"context"
	"strconv"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
)

// TestR_BAS_010_TheContactRowsOfARegistrationAreBounded: the name of a replica is what the Bastion reports, so the
// writer keeps the newest 256 rows of a registration and no more, and another registration's rows are untouched.
func TestR_BAS_010_TheContactRowsOfARegistrationAreBounded(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	bid, other := e.registration("partners"), e.registration("others")
	c := bastionmain.NewContacts(bastionmain.ContactOptions{DB: s.db, Audit: s.rec})
	ctx := context.Background()
	rep := bastionmain.Report{ClientAddress: "198.51.100.20", UserAgent: "trove-bastion/0.14.0", ResponseBaseURL: "https://packages.example.com"}
	if err := c.Record(ctx, bastionmain.Recognition{Bastion: "others", BastionID: other, Interface: iface.InterfaceVersion, Replica: "other-0"}, rep); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		rec := bastionmain.Recognition{Bastion: "partners", BastionID: bid, Interface: iface.InterfaceVersion, Replica: "replica-" + strconv.Itoa(i)}
		if err := c.Record(ctx, rec, rep); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.number("SELECT count(*) FROM bastion_contact WHERE bastion_id = $1::uuid", bid); n != 256 {
		t.Errorf("%d rows for the registration, want 256", n)
	}
	if n := e.number("SELECT count(*) FROM bastion_contact WHERE bastion_id = $1::uuid AND replica = 'replica-299'", bid); n != 1 {
		t.Error("the newest replica was trimmed")
	}
	if n := e.number("SELECT count(*) FROM bastion_contact WHERE bastion_id = $1::uuid", other); n != 1 {
		t.Errorf("another registration has %d rows", n)
	}
}
