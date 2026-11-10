package config

import "time"

// Ansible is the ansible section (R-ANS-008, R-ANS-009, R-ANS-020, R-ANS-031): the bounds and the retention of the Ansible Galaxy
// format. The per-repository settings of the format (ansible.authUrl, ansible.clientId) are in the repository document, not here.
type Ansible struct {
	MaxArchiveBytes     ByteSize      `yaml:"max_archive_bytes" default:"1GiB" validate:"min=1MiB,max=16GiB" doc:"Largest collection archive a publish or an upload through /files/ accepts; a larger body is refused with 413 payload_too_large before it is read when its length is declared, and while it is read otherwise (R-ANS-008, R-ANS-031). The upload limit of the system and of the repository (R-STOR-004) applies as well."`
	RoleArchiveMaxBytes ByteSize      `yaml:"role_archive_max_bytes" default:"256MiB" validate:"min=1MiB,max=4GiB" doc:"Largest role archive a remote repository fetches from the host of a role's version and keeps (R-ANS-020, R-ANS-031)."`
	ImportTaskKeep      time.Duration `yaml:"import_task_keep" default:"720h" validate:"min=24h,max=8760h" doc:"How long an import task of a publish stays readable; the daily task ansible-import-task-sweep deletes older ones and keeps the versions they describe (R-ANS-009). It stays far above the longest wait of a client, which retries a task that answers 404 until its own timeout."`
}
