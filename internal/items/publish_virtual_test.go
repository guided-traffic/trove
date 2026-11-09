package items

import "testing"

// Q-STOR-180: generated items go to a local or a virtual repository and to no other class.
func TestQ_STOR_180_PublishTargetClasses(t *testing.T) {
	for class, ok := range map[string]bool{"local": true, "virtual": true, "remote": false, "": false} {
		if err := checkPublishTarget(RepoRef{Class: class}); (err == nil) != ok {
			t.Errorf("class %q: err = %v, want accepted %v", class, err, ok)
		}
	}
}
