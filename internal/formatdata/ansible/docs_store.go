package ansible

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/meta"
)

// docsStore is the documentation group of the data port (R-ANS-033 to R-ANS-036, the interface ansible.DocsStore): the statements on
// ansible_version_docs, ansible_doc_entries, ansible_doc_files and ansible_doc_fetches (migrations ansible_doc_tables and ansible_doc_fetches). The contract is
// landed; the statements are not, and every method answers not_implemented, so nothing is silently empty (see errNotBuilt). The step that
// builds the group replaces this file and no other.
type docsStore struct{ *group }

// PutDocs implements ansible.DocsStore.
func (docsStore) PutDocs(context.Context, meta.Tx, int64, string, string, rows.DocSet) error {
	return errNotBuilt("the storage of documentation")
}

// RemoveDocs implements ansible.DocsStore.
func (docsStore) RemoveDocs(context.Context, meta.Tx, int64, string, string) error {
	return errNotBuilt("the removal of documentation")
}

// SetDocFetch implements ansible.DocsStore.
func (docsStore) SetDocFetch(context.Context, meta.Tx, int64, string, string, string, string) error {
	return errNotBuilt("the documentation fetch row")
}

// EnqueueDocsFill implements ansible.DocsStore.
func (docsStore) EnqueueDocsFill(context.Context, meta.Tx, int64, string, string) error {
	return errNotBuilt("the documentation fetch job")
}

// DocState implements ansible.DocsStore.
func (docsStore) DocState(context.Context, int64, string, string) (rows.DocState, error) {
	return rows.DocState{}, errNotBuilt("the documentation state")
}

// DocTree implements ansible.DocsStore.
func (docsStore) DocTree(context.Context, int64, string, string, *rows.DocQuery) (rows.DocTree, error) {
	return rows.DocTree{}, errNotBuilt("the documentation tree")
}

// DocEntry implements ansible.DocsStore.
func (docsStore) DocEntry(context.Context, int64, string, string, string, string) (rows.DocEntry, bool, error) {
	return rows.DocEntry{}, false, errNotBuilt("the documentation entries")
}

// DocFile implements ansible.DocsStore.
func (docsStore) DocFile(context.Context, int64, string, string, string) (rows.DocFile, bool, error) {
	return rows.DocFile{}, false, errNotBuilt("the documentation files")
}

// DocSet implements ansible.DocsStore.
func (docsStore) DocSet(context.Context, int64, string, string) (rows.DocSet, bool, error) {
	return rows.DocSet{}, false, errNotBuilt("the documentation set")
}

// RebuildDocs implements ansible.DocsStore.
func (docsStore) RebuildDocs(context.Context, int64, rows.ArchiveReader, string) (string, bool, error) {
	return "", false, errNotBuilt("the documentation reindex")
}

// RebuildRemoteDocs implements ansible.DocsStore.
func (docsStore) RebuildRemoteDocs(context.Context, int64, string) (string, bool, error) {
	return "", false, errNotBuilt("the documentation reindex of a remote")
}
