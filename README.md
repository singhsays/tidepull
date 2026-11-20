# tidepull

tidepull keeps local directories in sync with cloud storage. It pulls
changes from the cloud on a short polling cycle. Each cycle fetches only
what changed since the last cycle, through the provider's change API. It
runs headless on servers and NAS devices.

## Features

- **Google Drive and Dropbox** — more providers are planned.
- **Multi-account** — each account is one sync job: a remote folder, a
  local directory, and its own interval.
- **File-ID based** — renames and moves become local renames. Content is
  downloaded only when a file changes.
- **Integrity-checked** — downloads land as temp files. tidepull verifies
  the provider's content hash before renaming a file into place.
- **Own OAuth credentials** — each user creates their own OAuth app.
  tidepull handles login and token refresh.
- **Exclusions** — skip file types the provider cannot sync, plus
  additional MIME types per account.
- **Google Docs export (optional)** — Google-native files (Docs, Sheets,
  Slides) have no downloadable content. tidepull skips them by default.
  With the `export` option, selected types export as text, CSV, PDF, or
  SVG.
- **Deletion archive (optional)** — with `on-deletion: archive`, files
  the remote deletes move into the account's `.tidepull-archive`
  directory instead of being removed: one UTC-timestamped folder per
  deletion, with the original relative path preserved. The canonical
  tree stays a clean mirror; the archive keeps copies the remote no
  longer has. Archived copies are never cleaned up.

## Install

Download a binary from
[releases](https://github.com/singhsays/tidepull/releases) — Linux and
macOS, amd64 and arm64 — or install with Go:

```
go install github.com/singhsays/tidepull/cmd/tidepull@latest
```

## Quick start

1. Create an OAuth app for your provider and put the client ID and
   secret in a JSON file:

   ```json
   {"client_id": "your-app-key", "client_secret": "your-app-secret"}
   ```

   For Google, `credentials-file` can point at the downloaded
   client-secret JSON instead.

2. Write a config file. See `config.example.yaml` for all options:

   ```yaml
   accounts:
     - name: photos
       provider: google
       credentials-file: /path/to/credentials.json
       local-dir: /srv/photos
       folder-id: 1AbCdEf_GhiJkLmNoPqRsTuVwXyZ
   ```

3. Authorize the account in a browser:

   ```
   tidepull auth login photos -c config.yaml
   ```

4. Run one pull cycle, or run the daemon until interrupted:

   ```
   tidepull run -c config.yaml
   tidepull daemon -c config.yaml
   ```

Dropbox apps require a pre-registered redirect URI. Register
`http://127.0.0.1:53683/` in your app and set `redirect-port: 53683` on
the account.

## Notes

- tidepull syncs one way only: the cloud wins. Local edits are
  overwritten.
- Google converts some uploaded text files into Docs when the Drive
  setting "Convert uploads" is on. Converted files have no downloadable
  content; disable the setting to sync text files as files, or use the
  `export` option.
- Tokens and sync state are stored in
  `$XDG_DATA_HOME/tidepull/state.db`.

## Roadmap

- More providers

## License

[MIT](LICENSE)
