# Push protocol v2

Require `Authorization: Bearer <shared-secret>` on both endpoints. Return 401 for an invalid/inactive receiver or secret before reading the body. Never include secrets in URLs. Upgrade both components; v1 snapshots are rejected.

## Snapshot

`POST /api/navidrome_nowplaying/{receiver_id}` with `Content-Type: application/json`.

[Snapshot fixture](fixtures/snapshot.json). Required fields:

- `version`: integer `2`.
- `instance_id`: random sender identity, 1–128 UTF-8 bytes, no NUL.
- `sequence`: integer, 1 through `2^63 - 1`.
- `snapshot_at`: UTC ISO timestamp ending in `Z` or `+00:00`; ≤64 characters; ≤5 minutes in the future.
- `selection_status`: `selected`, `no_match` or `ambiguous`.
- `matching_stream_count`: integer 0–128, after source filters.
- `selected_stream`: one object when selected, otherwise null.
- `filters`: object containing `playback_username` and `player_name`, each null or a string ≤4096 UTF-8 bytes without NUL.

`selected` requires a stream and count ≥1; `no_match` requires null and count 0; `ambiguous` requires null and count ≥2. The Navidrome plugin owns selection; HA validates and displays it. No candidate list is sent.

Body limit: 1 MiB. Reject oversized source collections; never truncate. Capture `snapshot_at` with the position sample and retain it in cached heartbeats.

Selected stream fields:

- `playerId`: required nonempty stable callback identity, ≤4096 UTF-8 bytes. Do not use `getNowPlaying`'s numeric list index.
- `id`, `title`, `artist`, `album`, `coverArt`, `username`, `playerName`: optional strings, ≤4096 UTF-8 bytes.
- `state`: optional `playing`, `paused`, `starting`, `stopped` or `idle`.
- `duration`, `positionMs`: optional finite nonnegative integral numbers, ≤`2^53`; seconds and milliseconds respectively.
- `playbackRate`: optional finite nonnegative number, ≤`2^53`.

Optional fields may be absent or null. Strings must not contain NUL. Reject booleans as numbers. Retry identical contents for an instance/sequence. Keep only the newest replacement queued separately; send it after acknowledgement.

## Filter handoff

Existing HA filters must match the source-declared `filters` before its first accepted v2 snapshot. Mismatch returns HTTP 409 without accepting playback. After matching, HA records the migration and stops consulting the saved filters. Subsequent source-filter changes do not require HA reconfiguration.

## Ordering and liveness

- Fresh receiver after restart/reload: accept a running sender at any valid sequence.
- Established receiver: require sequence 1 for a new instance; reject retired identities.
- After 1024 retired identities: reject further transitions until reload rather than evict replay protection.
- Same instance, higher sequence: replace state and refresh liveness.
- Duplicate sequence: acknowledge current state, retain contents and refresh liveness.
- Lower sequence or rejected instance: retain state; do not refresh liveness.
- Send heartbeats every 90 seconds. Mark unavailable after 300 seconds without accepted metadata.
- Clear ordering state and artwork cache on receiver reload/unload.

## Acknowledgement

HTTP 200 body: [acknowledgement fixture](fixtures/ack.json).

Require `accepted: true`, matching `instance_id` and matching `accepted_sequence` before committing delivery. HTTP 200 alone is insufficient. On ordering rejection, generate a new instance and retry sequence 1.

`missing_artwork`: zero or one ID, restricted to selected uncached artwork; empty on rejection.

## Artwork

`POST /api/navidrome_nowplaying/{receiver_id}/artwork` with raw image bytes and `X-Navidrome-Artwork-ID: <coverArt>`.

- Content type: `image/png`, `image/jpeg`, `image/webp` or `image/gif`; require a matching image signature.
- Body limit: 10 MiB, including chunked uploads.
- Accept only currently requested IDs. Invalidate in-flight uploads on instance change, removal from the requested set, or receiver reload/rotation.
- Success: HTTP 200 with `{}`. Do not refresh liveness.
- Cache limit: 16 images / 20 MiB in memory; clear on reload/unload.

## Responses

- Both endpoints: 400 invalid body; 401 invalid secret/receiver; 413 oversized body; 415 unsupported content type.
- Snapshot ordering rejection: HTTP 200 with `accepted: false`.
- Pending filter handoff mismatch or stale/unrequested artwork: HTTP 409.

Use bounded retries and disable redirects. [Sender limits](../navidrome-plugin/README.md#delivery-failures-and-limits).
