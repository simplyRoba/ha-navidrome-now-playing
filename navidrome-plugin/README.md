# Navidrome push plugin

[Installation and pairing](../README.md#quick-start).

## Docker build

Requires Docker and POSIX shell. From the repository root:

```sh
./navidrome-plugin/build.sh
```

Output: `build/navidrome-plugin/navidrome-nowplaying.ndp`. `build/` is gitignored. No running Navidrome container is needed; the script does not install or copy into one.

Optional host output directory:

```sh
./navidrome-plugin/build.sh /absolute/output/directory
```

Build failures retain the previous package. Mount only `build/navidrome-plugin/navidrome-nowplaying.ndp` at `/data/plugins/navidrome-nowplaying.ndp:ro`; keep other plugins and the configured plugin folder unchanged.

> [!IMPORTANT]
> Recreate Navidrome after every rebuild, then rescan and re-enable the plugin if its binary changed. Rescan alone does not refresh the single-file bind.

## Native build

Requires Go 1.25+, `make`, and Python 3 for tests/verification. SDK pin: Navidrome 0.64.2 commit `101145742f47`; target: WASI p1.

From `navidrome-plugin/`:

```sh
make test
make
make verify
```

Set `GO=/absolute/path/to/go` for a custom executable. Output uses the same `build/navidrome-plugin/` directory at the repository root. `make clean` removes this plugin's generated files, not other packages or the directory.

## Selection and filters

The plugin tracks reports by username and stable player ID. Username and Player name filters are exact and case-sensitive; both must match when set. Leave Player name empty to follow changes between players.

Within one user, the newest `playing` or `starting` report wins. Paused players are considered only when none are active. Pause, stop and expiry reports update only their own player. If different matching users are active, selection is ambiguous; use a username filter to choose one.

Reports and expiry state persist across restarts. A fresh installation waits for its first playback report. Playing sessions expire after the remaining track duration plus five seconds, adjusted for playback rate; paused sessions expire after 30 minutes.

## Delivery failures and limits

- Missing playback events: check assigned users, library access and scrobble filters. Supported triggers: `reportPlayback` and legacy `scrobble(submission=false)`.
- Playback-store errors: old heartbeats are suspended until recovery. More than 128 matching players fails rather than truncates.
- Artwork errors: metadata delivery continues. No plugin-side timeout for internal resizing.
- Timing: one-shot timers for playback reports (coalesced within one second), source expiry, retries and 90-second heartbeats. No recurring idle clock. Retry backoff is 2–32 seconds; HTTP timeout is seven seconds; delivery lease is 120 seconds.
- Limits: 128 matching players, one selected stream / 1 MiB per snapshot, 2 MiB playback store, 4 MiB host JSON, 10 MiB per image, two artwork attempts per tick.

[Protocol contract](../protocol/README.md).

## Security

Use HTTPS with a certificate trusted by Navidrome outside a trusted network. Redirects are disabled. Set Push URL to the full receiver URL: no trailing slash, query, fragment or embedded credentials.

Review `requiredHosts: ["*"]` before granting HTTP permission. To restrict outbound access, replace it with your HA host/IP/CIDR before packaging. Protect the saved secret and plugin settings.

Host references: [scheduler](https://github.com/navidrome/navidrome/blob/v0.64.2/plugins/host_scheduler.go), [legacy playback callbacks](https://github.com/navidrome/navidrome/blob/v0.64.2/server/subsonic/media_annotation.go).
