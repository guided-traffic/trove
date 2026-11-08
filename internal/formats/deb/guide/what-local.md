A local repository holds the Debian packages your teams upload{{if .Repository}} to `{{.Repository}}`{{end}}. It is an APT repository in the Debian repository format, so `apt`, `apt-get` and every tool that reads an APT source use it: Debian, Ubuntu and their derivatives.

### Suites and components

A package file is listed in one or more **suites** (the `distribution` of an upload: `stable`, `testing`, `noble`) and **components** (`main` unless the upload says otherwise). One file can be listed in several suites; moving a package from `testing` to `stable` changes its coordinates and does not upload it again. Both names are lower case letters, digits and `.`, `_`, `+`, `-`, at most 64 characters.

### The index and its signature

trove builds the index files of every suite (`Packages` and `Release`) from what has been uploaded and signs them with a key that belongs to this repository alone. A client trusts that key for this repository's source line and for nothing else. An upload does not wait for the index: the answer carries a `changeSeq` per suite, and the index state read waits until the published index covers it.

### What an upload must be

A package file is read before it is stored. Its control data is checked against the grammar APT depends on (the package name, the version, the architecture), and a file that trove could not list in an index is refused instead of stored. The package name and version a file declares are what authorization and ownership decide on. A package, version and architecture is stored once: other bytes under the same identity are refused, and the same bytes again only add the coordinates of the request.

### Who may upload a name

The first upload of a package name records its owner. From then on only owners and holders of the manage permission upload, move or delete that name. A repository can turn package ownership off.
