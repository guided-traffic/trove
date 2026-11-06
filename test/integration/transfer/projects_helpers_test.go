//go:build integration

package transfer

// project inserts a project row. It runs before the replica that must know it starts: the decision point builds
// its index from PostgreSQL when it starts.
func (e *webEnv) project(key string) {
	e.t.Helper()
	e.exec(`INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), $1, $1) ON CONFLICT (key) DO NOTHING`, key)
}
