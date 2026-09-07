# Vendored VoHive Backends

This directory stores fixed binary inputs for desktop release packaging.

## iniwex5/vohive backup runtime

- File: `iniwex5-vohive-v1.5.5-10-gf9eb85d_linux_amd64`
- Packaged as: `desktop/src-tauri/resources/vohive/vohive-orson-v1.5.5_linux_amd64`
- Source mirror: `Orson-Yan/Vohive-155`, `release/vohive_v1.5.5-10-gf9eb85d_linux_amd64`
- SHA256: `841d117d4921718b2627a6485b09c62d858c088e42e6e55468ae0f3e0ece1bdd`

The user-facing runtime name is `iniwex5/vohive`; the Orson/Vohive-155 name is kept only as source provenance for this mirrored binary.

VoCat is not vendored in this directory. The desktop release workflow downloads the latest VoCat Linux amd64 runtime into `desktop/src-tauri/resources/vocat/` before packaging.

Portable release packages include additional binary notices at
`desktop/src-tauri/resources/vohive/THIRD_PARTY_BINARY_NOTICES.md`.
