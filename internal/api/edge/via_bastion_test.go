package edge

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// TestR_BAS_010_AForwardAndACheckAreViaABastion: the subject of a recognized forward and of the caller of a check carries ViaBastion, so
// the level internal does not read for it; an interface route decides no caller and a request without either is not marked (R-BAS-010,
// Q-BAS-148).
func TestR_BAS_010_AForwardAndACheckAreViaABastion(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"an ordinary request", context.Background(), false},
		{"a recognized forward", bastionmain.WithRecognition(context.Background(), bastionmain.Recognition{Bastion: "partners"}), true},
		{"an interface route", bastionmain.WithRecognition(context.Background(), bastionmain.Recognition{Bastion: "partners", Route: true}), false},
		{"the caller of a check", httpserver.WithBastionForward(context.Background()), true},
	}
	for _, c := range cases {
		t.Run("R-BAS-010 "+c.name, func(t *testing.T) {
			if got := viaBastion(c.ctx); got != c.want {
				t.Errorf("viaBastion = %v, want %v", got, c.want)
			}
			if got := SubjectOf(c.ctx).ViaBastion; got != c.want {
				t.Errorf("Subject.ViaBastion = %v, want %v", got, c.want)
			}
		})
	}
}
