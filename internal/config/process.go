package config

import "slices"

// The roles a replica runs in (R-ARCH-001, Q-ARCH-294, Q-ARCH-295).
const (
	// RoleAPI serves the route families ui, api and ci_exchange and holds the sessions and the login secrets.
	RoleAPI = "api"
	// RolePackages serves the route families packages and files, with tokens only.
	RolePackages = "packages"
	// RoleWorker claims jobs and serves no client listener.
	RoleWorker = "worker"
	// RoleWeb is api and packages in one process: the bridge of the upgrade across the cut (Q-ARCH-295).
	RoleWeb = "web"
	// RoleAll is every role but the Bastion in one process, for local development.
	RoleAll = "all"
)

// ValidRole reports whether role is one of the values a replica registers with, the value of --role, of the replica
// registry, of trove_build_info{role} and of the `role` of every log record (R-OPS-003, R-OPS-006, Q-OPS-161): one
// vocabulary for all of them.
func ValidRole(role string) bool {
	switch role {
	case RoleAPI, RolePackages, RoleWorker, RoleWeb, RoleAll:
		return true
	}
	return false
}

// Process selects what one replica runs (R-ARCH-001).
type Process struct {
	// Role has no default: the chart sets it per Deployment and `trove serve
	// --role` overrides it.
	Role string `yaml:"role" validate:"oneof=api|packages|worker|web|all" doc:"Role of this replica: api, packages, worker, web (the bridge of the upgrade across the cut) or all (local development only). No default."`
}

// RoleError is the message `trove serve` prints when no valid role is set.
const RoleError = "process.role must be api, packages, worker, web or all"

// ServesClients reports whether a role opens client listeners: every role but worker.
func ServesClients(role string) bool {
	switch role {
	case RoleAPI, RolePackages, RoleWeb, RoleAll:
		return true
	}
	return false
}

// ValidateFor requires a role where one is run.
func (p *Process) ValidateFor(cmd Command, r *Report) {
	if cmd != CommandServe {
		return
	}
	switch p.Role {
	case RoleAPI, RolePackages, RoleWorker, RoleWeb, RoleAll:
	default:
		r.Raw(RoleError)
	}
}

// RoleFamilies returns the route families a role serves (R-EP-005, Q-ARCH-298), in the fixed order of [Surfaces]: api the
// families ui, api and ci_exchange, packages the families packages and files, web and all every family, worker none. A
// process registers on each endpoint the families of its surface that its role serves.
func RoleFamilies(role string) []string {
	switch role {
	case RoleAPI:
		return []string{SurfaceUI, SurfaceAPI, SurfaceCIExchange}
	case RolePackages:
		return []string{SurfacePackages, SurfaceFiles}
	case RoleWeb, RoleAll:
		return Surfaces()
	}
	return nil
}

// IntersectFamilies returns the families of surface that a role's families hold, in the order of surface: the effective
// surface of an endpoint on a process (Q-ARCH-298). An empty result means the process opens no listener for the endpoint.
func IntersectFamilies(surface, role []string) []string {
	var out []string
	for _, f := range surface {
		if slices.Contains(role, f) {
			out = append(out, f)
		}
	}
	return out
}
