// Package merge is the pure merge of the version lists and collection documents that the members of a virtual Ansible
// repository serve (R-ANS-022). It reads no store and no network: the caller hands it the members' parts and it returns
// the merged list, the merged collection facts and the digest conflicts to count.
//
// The package is a leaf: it imports the galaxydoc and version leaves of the format and the standard library.
package merge
