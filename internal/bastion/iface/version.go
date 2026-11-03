package iface

// InterfaceVersion is the version of the Bastion interface (R-BAS-009,
// Q-BAS-20, Q-BAS-26): a positive integer that starts at 1 and is raised by every
// change of the interface, whatever its kind. Both sides carry it in
// [HeaderInterface]; main installation and Bastion talk only when the two values
// are equal.
//
// A change of this value belongs in the same change as the regenerated
// testdata/interface.txt (go test ./internal/bastion/iface -run TestR_BAS_009
// -update-interface), and the release notes announce it.
const InterfaceVersion = 1
