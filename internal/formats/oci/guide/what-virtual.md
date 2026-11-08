A virtual repository{{if .Repository}} `{{.Repository}}`{{end}} gives clients one address for several repositories: local repositories with your own images, and remote repositories that stand for upstream registries. A client pulls from the virtual repository and trove answers from the first member that has the image.

### How a pull is resolved

- A **tag** is taken from the first member that has it: local members first, in member order, then remote members in member order. A remote member is consulted through the normal remote request path, so an upstream is contacted for a caller who may fill the cache.
- A **digest** is taken from wherever the identical bytes are found.
- The **tags list** of an image name is the union of the members' tags, sorted and without duplicates. The **referrers** of a digest are the union of the members' referrers.
- The response header `Trove-Resolved-From` names the member that answered.

An administrator can claim image names for a member (`acme/*` for a local repository). A claimed name is never served from another member, which keeps a public image of the same name from replacing your own.

### Pushing

A push, a layer upload and a mount through a virtual repository are executed on its deployment target. A virtual repository without a target takes no push: it is refused (`403` `DENIED`), and a token for it carries no `push`. When you push an image that is based on a layer a remote member has cached, trove links that layer into the target instead of asking the client to upload it again. Deleting through a virtual repository is not offered (`405` `UNSUPPORTED`): delete in the repository that holds the image.
