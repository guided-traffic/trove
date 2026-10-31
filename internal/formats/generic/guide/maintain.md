{{if not .Anonymous}}### Replacing a file

Upload the new bytes to the same path. The upload answers `201` like a new file and needs the overwrite permission besides deploy; without it trove answers `403` and keeps the stored file. With `If-None-Match: *` the upload never replaces anything.

### Deleting

`trove_curl --fail --request DELETE "{{.RegistryURL}}<path>"` deletes a file and answers `204`. The address of a folder deletes the folder with everything below it. That needs the delete permission on every item in it: when it is missing for one, trove answers `403` and deletes nothing. A folder of more than 10,000 items (the default limit) is deleted by a background job: trove answers `202` with the address of the job in `Location`, and the files disappear as the job runs.

A delete is final: trove has no trash can yet. The root of the repository cannot be deleted this way (`400` `invalid_path`); an administrator deletes the repository instead.
{{end}}