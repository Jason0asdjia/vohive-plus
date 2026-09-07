# VoCat Runtime Resources

Local desktop builds and GitHub Actions download the latest `MengMengCode/VoCat` Linux amd64 release into this directory before building the desktop package.

Generated files are ignored by Git:

- `vocat-linux-amd64`
- `SHA256SUMS`
- `VOCAT_VERSION`
- `LICENSE`

The sync script also downloads VoCat's upstream `LICENSE` into this directory
so the portable package carries the third-party runtime terms.

Local development may provide `VOHIVE_VOCAT_SOURCE` and `VOHIVE_VOCAT_VERSION` when running `pnpm sync:vocat`.
