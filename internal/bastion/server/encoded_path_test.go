package server

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

// TestR_BAS_003_R_BAS_006_AnEncodedSpellingOfTheInterfaceRoutesCannotTurnTheReplicaUnreachable: the main
// installation's router decodes the segments of a path, so /api/%761/bastion/handshake is the handshake there.
// Sent through the client listener it is answered 404 bastion_not_served by the Bastion, reaches the main
// installation never, and the replica stays reachable however often it is repeated (Q-BAS-121, security finding
// P1: the answer 401 bastion_token_invalid to it was read as a refusal of the Bastion's own token).
func TestR_BAS_003_R_BAS_006_AnEncodedSpellingOfTheInterfaceRoutesCannotTurnTheReplicaUnreachable(t *testing.T) {
	main := maintest.New(t)
	main.SetAnonymous(true)
	dir := t.TempDir()
	srv, logs := newLinked(t, linkedConfig(main, dir, tokenFileFor(t, dir, main.NewToken()), ""), nil)
	run(t, srv, logs)
	eventually(t, 10*time.Second, "reachable", func() bool { st := srv.link.State(); return st.Reachable && st.Handshaken })
	base := "http://" + srv.ClientAddr().String()
	forwards := len(main.Forwards())
	for i := 0; i < 5; i++ {
		for _, p := range []string{"/api/%761/users", "/api/%76%31/bastion/handshake", "/api/%761/bastion/feed"} {
			code, _, body := get(t, base+p)
			if code != 404 || !strings.Contains(body, "bastion_not_served") {
				t.Fatalf("%s: %d %s", p, code, body)
			}
		}
		time.Sleep(100 * time.Millisecond)
		if !srv.link.State().Reachable {
			t.Fatalf("the replica turned unreachable after an encoded interface route:\n%s", logs.String())
		}
	}
	if got := len(main.Forwards()); got != forwards {
		t.Fatalf("the main installation saw %d forwards of the encoded paths", got-forwards)
	}
	if body := metricsBody(t, srv); !strings.Contains(grepLines(body, "trove_bastion_main_token_refused"), "trove_bastion_main_token_refused 0") {
		t.Fatalf("trove_bastion_main_token_refused moved: %s", grepLines(body, "trove_bastion_main_token_refused"))
	}
}
