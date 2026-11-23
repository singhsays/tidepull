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

Linux amd64 release images are also published to ghcr.io (`vX.Y.Z` and
`latest` tags). The image runs as a non-root user by default; run it
with a user matching the local directory's ownership:

```
docker run -d --name tidepull --user 1000:1000 \
  -p 127.0.0.1:8420:8420 \
  -v /srv/documents:/srv/documents \
  ghcr.io/singhsays/tidepull:v0.2.0 daemon -c /etc/tidepull/config.yaml
```

Inside a container, set `server.listen` to `0.0.0.0:8420`: the default
binds the loopback interface only, which a published port cannot reach.

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

Some providers require a pre-registered redirect URI. Register
`http://127.0.0.1:58914/` (the default) in your app. Set
`redirect-port` on the account only when your app registered a
different port.

## Status

In daemon mode, tidepull serves status showing configured accounts and
last sync status. It serves at http://127.0.0.1:8420 by default.

`/healthz` returns 200 as long as the server is up, for load balancer
or init-system liveness probes.

### Authorizing from the status page

Each account row on the status page has an Authorize link while the
account has no token, and Reauthorize plus Disconnect once it does.
Authorize starts the provider's OAuth consent flow and, on completion,
stores the account's token; the next sync cycle picks it up. Disconnect
removes the token and the change-stream position; the next cycle reports
the account as unauthorized, and a later authorization starts from a full
resync.

After a completed authorization the browser returns to the status page
automatically.

How the provider sends the browser back depends on the account's
`redirect-url` setting:

- **Reverse-proxy mode** (the account sets `redirect-url`): the daemon
  redirects the browser to the provider's consent page, and the provider
  sends the browser back to that URL. Serve it over https and have your
  reverse proxy forward it to the daemon's `/callback` route — the
  provider itself never connects to the daemon, only your browser does.
- **Paste mode** (default): the redirect goes to
  `http://127.0.0.1:58914/` (or `http://127.0.0.1:<redirect-port>/` when
  the account pins `redirect-port`). On a headless host that page may
  fail to load; that is fine. Copy the full URL from the browser's
  address bar after approving — it starts with the redirect address and
  carries the completion parameters — and paste it into the form on the
  authorization page.

The provider app must have both redirect URIs registered: the loopback
one (`http://127.0.0.1:58914/`) and the https `redirect-url`. Some
providers match any loopback port; others require the exact
pre-registered address, so pin it with `redirect-port`.

Pending authorizations expire after about ten minutes; start again if
one lapses.

To change the listen address or disable it, use the top-level `server` config
block:

```yaml
server:
  listen: 127.0.0.1:8420
  enabled: true
```

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
