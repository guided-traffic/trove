package config

import "time"

// Deb is the deb section (R-DEB-010, R-DEB-012, R-DEB-014, R-DEB-026): the timing of the index builds of the Debian (APT)
// format. The per-repository settings of the format (deb.architectures, deb.compression, deb.upstreamKeys, deb.mirror, ...) are in
// the repository document, not here.
type Deb struct {
	IndexDelay           time.Duration `yaml:"index_delay" default:"2s" validate:"min=1s,max=60s" doc:"Time a change of a suite waits before the build of its indexes starts, so a burst of uploads is one build (R-DEB-010). The delay bound of an upload to its published index is this value plus 5 s (jobs.poll_interval more while Valkey is unavailable). At least 1 s, because a Release Date and a signature time trail the database clock by a minute and a build per second is the most a suite can publish (Q-DEB-30)."`
	VirtualIndexInterval time.Duration `yaml:"virtual_index_interval" default:"60s" validate:"min=10s,max=1h" doc:"Least time between two builds of a virtual repository's indexes that a local member's publication triggers (R-DEB-026); a change of the members or the settings builds without this delay."`
	ByHashKeep           time.Duration `yaml:"by_hash_keep" default:"1h" validate:"min=10m,max=168h" doc:"How long the files of a superseded index generation stay served by hash, so a client that read InRelease before a build can still fetch the index files it names (R-DEB-012); at least three generations are kept in any case."`
	IndexWaitMax         time.Duration `yaml:"index_wait_max" default:"30s" validate:"min=1s,max=120s" doc:"The longest wait of an index state read with waitFor (R-DEB-014); a longer request answers with the state it has reached."`
}
