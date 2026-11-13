<p align="center">
  <img src="custom_components/navidrome_nowplaying/brand/icon.png" alt="Navidrome" width="128" />
</p>

<h1 align="center">Navidrome Now Playing</h1>

<p align="center">
  <a href="https://conventionalcommits.org"><img src="https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg" alt="Conventional Commits" /></a>
  <a href="https://github.com/simplyRoba/ha-navidrome-now-playing/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/simplyRoba/ha-navidrome-now-playing/ci.yml" alt="GitHub Workflow Status" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/simplyRoba/ha-navidrome-now-playing" alt="License" /></a>
  <a href="https://github.com/simplyRoba/ha-navidrome-now-playing/releases"><img src="https://img.shields.io/github/v/release/simplyRoba/ha-navidrome-now-playing" alt="Release" /></a>
</p>

Display Navidrome playback in Home Assistant as a `media_player` entity.

## Features

- Track title, artist, album and artwork
- Playback state, position and duration
- Username and player filters
- Playback updates pushed from Navidrome without API polling

The entity is read-only. Playback controls remain in your music client.

## Requirements

- Home Assistant 2026.2.3+ with HACS
- Navidrome 0.64.2+ with plugins enabled
- A Home Assistant URL reachable from Navidrome

## Quick start

### Home Assistant

1. In HACS, open **Custom repositories**.
2. Add `https://github.com/simplyRoba/ha-navidrome-now-playing` with type **Integration**.
3. Download **Navidrome Now Playing**.
4. Restart Home Assistant.

### Navidrome

1. Download `navidrome-nowplaying.ndp` from the [latest release](https://github.com/simplyRoba/ha-navidrome-now-playing/releases/latest).
2. Place it in Navidrome’s configured plugins folder.
3. Open **Plugins → Rescan**.
4. Keep **Home Assistant Now Playing** disabled until pairing is complete.

### Pairing

1. In Home Assistant, open **Settings → Devices & services → Add integration → Navidrome Now Playing**.
2. Enter the Home Assistant URL reachable from Navidrome.
3. Copy the displayed **Push URL** and **secret** into the Navidrome plugin settings.
4. Grant the plugin access to the account used by your playback client, approve its permissions and save.
5. Select **Finish** in Home Assistant, then enable the Navidrome plugin.

Play music and check the entity’s track metadata and artwork.

## Configuration

Configure playback filters in the Navidrome plugin.

| Field | Description |
| --- | --- |
| Push URL | Receiver URL displayed during HA pairing. |
| Secret | Pairing secret displayed by HA. |
| Playback username | Optional exact, case-sensitive username. |
| Player name | Optional exact, case-sensitive player name. |

Both filters apply when set. Leave **Player name** blank to follow playback across players.

If different matching users are playing simultaneously, the entity stays idle. Set **Playback username** to select one user.

Use **Reconfigure** in Home Assistant to change its URL or generate a new pairing secret.

## Security

Pairing secrets are stored unencrypted in HA configuration and Navidrome plugin settings. Protect both, including their backups.

Use HTTPS with a certificate trusted by Navidrome outside a trusted network.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and tests, and the [plugin README](navidrome-plugin/README.md) for source builds.

Copyright (C) 2026 simplyRoba.

**This project is developed spec-driven with AI assistance, reviewed by a critical human.**
