# tidepull

tidepull keeps your local directories and your cloud storage in sync.

It synchronizes files between local directories and cloud services in near
real time. Cloud-side changes are pulled down to your folders on a short
polling cycle, using each provider's change API so every cycle fetches only
what changed. It runs headless on servers and NAS devices.

- **Cloud services**: Google Drive is supported today; more providers are
  planned (Dropbox next).
- **Multi-account**: each account is one sync job — a remote folder, a local
  directory, and its own interval and credentials.
- **File-ID based**: sync state is keyed on the provider's file IDs, so
  renames and moves become local renames and content is only re-downloaded
  when a file actually changes.
- **Own OAuth credentials**: bring your own client ID and secret; the
  daemon handles login and token refresh.
- **Integrity-checked**: downloads land as temp files and are verified
  against the provider's checksum before being renamed into place.

Status: early development. See `config.example.yaml` to get a feel for the
configuration.
