package link

import (
	"context"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// Guard wraps the client the request flow uses so that a refused Bastion token or an interface mismatch in any
// answer makes the link unreachable at once, whichever request met it (R-BAS-006, R-BAS-028). The link's own
// requests go through the unwrapped client and handle their errors themselves.
func (l *Link) Guard(c mainclient.Client) mainclient.Client { return guarded{c: c, l: l} }

type guarded struct {
	c mainclient.Client
	l *Link
}

func (g guarded) Handshake(ctx context.Context) (iface.Handshake, error) {
	hs, err := g.c.Handshake(ctx)
	g.l.Note(err)
	return hs, err
}

func (g guarded) Check(ctx context.Context, class mainclient.Class, req iface.CheckRequest) (iface.CheckAnswer, error) {
	ans, err := g.c.Check(ctx, class, req)
	g.l.Note(err)
	return ans, err
}

func (g guarded) Feed(ctx context.Context, after iface.Cursor, limit int) (iface.FeedAnswer, error) {
	ans, err := g.c.Feed(ctx, after, limit)
	g.l.Note(err)
	return ans, err
}

func (g guarded) Downloads(ctx context.Context, req iface.DownloadsRequest) error {
	err := g.c.Downloads(ctx, req)
	g.l.Note(err)
	return err
}

func (g guarded) Do(ctx context.Context, f mainclient.Forward) (*mainclient.Response, error) {
	resp, err := g.c.Do(ctx, f)
	g.l.Note(err)
	return resp, err
}
