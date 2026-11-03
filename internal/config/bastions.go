package config

import "time"

// Bastions configures the main installation's side of the trove Bastion (R-BAS-004, R-BAS-011, R-BAS-015,
// R-BAS-016): how many registrations exist, what one check may ask, how the revocation feed reads the audit
// trail and how download events are accepted. The Bastion's own configuration is BastionConfig; a Bastion
// reads nothing of this section.
type Bastions struct {
	MaxRegistrations int               `yaml:"max_registrations" default:"16" validate:"min=1,max=256" doc:"Bastion registrations that may exist; also bounds the bastion label of the interface metrics (R-BAS-004)."`
	Check            BastionsCheck     `yaml:"check"`
	Feed             BastionsFeed      `yaml:"feed"`
	Downloads        BastionsDownloads `yaml:"downloads"`
}

// BastionsCheck bounds the credential and read check (R-BAS-011).
type BastionsCheck struct {
	MaxRepositories int `yaml:"max_repositories" default:"16" validate:"min=1,max=256" doc:"Repository keys one check may ask about (R-BAS-011)."`
}

// BastionsFeed configures the revocation feed (R-BAS-015).
type BastionsFeed struct {
	Lookback time.Duration `yaml:"lookback" default:"5m" validate:"min=30s,max=1h" doc:"A feed read returns every entry newer than its cursor minus this period, so a transaction that committed late is not missed; longer than any transaction (the replay margin of R-HA-054)."`
	MaxPage  int           `yaml:"max_page" default:"1000" validate:"min=10,max=10000" doc:"Entries one feed read returns at most (R-BAS-015)."`
}

// BastionsDownloads configures the intake of download events (R-BAS-016).
type BastionsDownloads struct {
	MaxBatch int           `yaml:"max_batch" default:"1000" validate:"min=1,max=10000" doc:"Events one request of the intake accepts (R-BAS-016)."`
	MaxAge   time.Duration `yaml:"max_age" default:"720h" validate:"min=1h" doc:"An event older than this is not recorded and counts as dropped (R-BAS-016)."`
}
