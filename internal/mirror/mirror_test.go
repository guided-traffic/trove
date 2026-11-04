package mirror

import (
	"encoding/json"
	"testing"
)

// Contract test of R-REPO-053, R-REPO-056 (the mirror mode, not built yet): the IDs stay out of the test names, so the
// coverage evidence does not count a requirement whose engine is missing (Q-ROAD-5).
func TestMirrorNames(t *testing.T) {
	t.Run("the dedup key names the repository", func(t *testing.T) {
		if got := PassKey(42); got != "mirror-pass:42" {
			t.Fatalf("PassKey(42) = %q", got)
		}
	})
	t.Run("the arguments of a pass carry the repository and the full flag", func(t *testing.T) {
		b, err := json.Marshal(PassArgs{Repository: "npm-types", RepositoryID: 7, Full: true})
		if err != nil || string(b) != `{"repository":"npm-types","repositoryId":7,"full":true}` {
			t.Fatalf("%s, %v", b, err)
		}
	})
}
