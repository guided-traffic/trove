// Package iface is the Bastion interface: the wire contract between a trove
// Bastion and the main installation it serves (R-BAS-008 to R-BAS-017). Both
// sides import it, so it holds only types, constants and pure functions over the
// standard library, and no server dependency (R-BAS-001): the routes and headers
// of the interface, the marking of a forwarded response, the Trove-Bastion-Subject
// format, the feed cursor, the JSON bodies of the handshake, the check, the feed
// and the download intake, and the problem codes the two sides exchange.
//
// The interface has a version of its own, [InterfaceVersion] (R-BAS-009). Every
// change of any exported declaration of this package, a field, a constant, a
// header value or the meaning of one, raises it, and testdata/interface.txt
// records the surface so that TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion
// fails on a change that keeps the version. Equality of the two sides' versions
// is the rule, so neither side reads a field it does not know: [DecodeStrict]
// refuses an unknown member.
//
// The package is the one place the two sides share. Nothing here talks to
// PostgreSQL, the network or the file system.
package iface
