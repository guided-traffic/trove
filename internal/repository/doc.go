// Package repository is the repository model (R-REPO-001 to R-REPO-013): class,
// format and key, the configuration document (R-REPO-004), the in-process
// snapshot that is rebuilt on the change area repo_config, and the key rules.
//
// This file set holds only what several areas share: the model types and the
// read interface that authz, the items API and the files transfer use. The
// store, the snapshot loader, the management API (WP1.4) and the generic local
// class are the repository owner's. The package does not know formats: Format
// is the registered format name; internal/formats is above this layer.
package repository
