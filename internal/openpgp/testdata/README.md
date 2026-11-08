# Test data of internal/openpgp

`debian-archive-keyring.pgp` is `/usr/share/keyrings/debian-archive-keyring.pgp` of the package `debian-archive-keyring` 2025.1,
copied from the image `debian:13` (SHA-256 `506b815cbb32d9b6066b4a2aa524071e071761e7e7f68c3ac74f3061ba852017`): nine public
certificates, 55,918 bytes binary. The package's copyright file says "The keys in the keyrings don't fall under any copyright".
The test armors it (75,803 bytes) to the form an operator pastes into `deb.upstreamKeys`, so that the 256 KiB and 32 certificate
bounds of R-DEB-034 are tested with a real keyring of that size.
