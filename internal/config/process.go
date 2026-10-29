package config

// Process selects what one replica runs (R-ARCH-001).
type Process struct {
	// Role has no default: the chart sets it per Deployment and `trove serve
	// --role` overrides it.
	Role string `yaml:"role" validate:"oneof=web|worker|all" doc:"Role of this replica: web, worker or all (local development only). No default."`
}

// RoleError is the message `trove serve` prints when no valid role is set.
const RoleError = "process.role must be web, worker or all"

// ValidateFor requires a role where one is run.
func (p *Process) ValidateFor(cmd Command, r *Report) {
	if cmd != CommandServe {
		return
	}
	switch p.Role {
	case "web", "worker", "all":
	default:
		r.Raw(RoleError)
	}
}
