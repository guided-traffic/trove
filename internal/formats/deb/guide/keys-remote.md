The client verifies the release it receives, and a remote passes the upstream's release through, so the key the client trusts is the **upstream's**, never one of trove's.

- **The repository has `deb.upstreamKeys`.** trove verifies every release it stores against those keys, and `signing-key.asc` serves them{{if .RegistryURL}} (`{{.RegistryURL}}signing-key.asc`){{end}}. Install that file as the key of the source, as the snippet of the section above does. Compare its fingerprints with the ones the repository's page shows before you trust it.
- **The repository has none.** trove stores what the upstream sends and does not check it; your client does. Use the archive's own keyring: on Debian `/usr/share/keyrings/debian-archive-keyring.gpg`, on Ubuntu `/usr/share/keyrings/ubuntu-archive-keyring.gpg`. A remote without keys cannot be a mirror or a member of a virtual repository.

Do not use `trusted=yes` or `allow-insecure`: neither is needed, and both turn the check the client makes off.
