A generic local repository{{if .Repository}}, `{{.Repository}}`,{{end}} stores files of any kind at paths you choose: installers, archives, documents, build outputs, content of formats trove has no repository type for. trove does not look into a file. It keeps the bytes and the media type they were uploaded with, computes their digests, and serves them back as stored. There are no package names, versions or metadata documents, and no file is refused for what it contains.

Every address below `{{.RegistryURL}}` names a file or a folder:

- Whoever uploads chooses the path. Folders arise from the paths of the files in them, and a path names a file or a folder, never both.
- Paths are compared byte for byte: `Report.pdf` and `report.pdf` are two files.
- A path holds at most 4096 bytes and a segment at most 255 bytes, in UTF-8. Empty, `.` and `..` segments, backslashes and control characters are refused.
- The repository's include and exclude patterns can restrict the paths it takes. An upload outside them is refused (`403` `not_admitted`), and a file stored there before the patterns changed answers `404`.

### A file changes only with the overwrite permission

Uploading to a path that holds no file needs the deploy permission. Uploading the same bytes again changes nothing and answers `200`. Uploading other bytes to a path that holds a file replaces it, answers `201` like a new file, and needs the overwrite permission as well; without it the answer is `403` and the stored file stays. Whether a published file can change is therefore decided by who holds overwrite on the repository.

### A download is as trustworthy as its uploader

trove neither scans nor validates what a generic repository stores. A browser never runs a stored file as part of trove's pages: every file is served as an attachment, with a sandbox policy. A downloaded installer is still exactly as trustworthy as the account that uploaded it, so check it against a digest its publisher gave you (see the section on downloads).
