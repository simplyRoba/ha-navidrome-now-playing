"""Shared protocol fixtures and paired release metadata."""

import json
import tomllib
from pathlib import Path

from custom_components.navidrome_nowplaying.push import validate_snapshot

ROOT = Path(__file__).resolve().parents[1]


def test_shared_snapshot_fixture():
    value = json.loads((ROOT / "protocol/fixtures/snapshot.json").read_text())
    instance, sequence, timestamp, selection, filters = validate_snapshot(value)
    ack = json.loads((ROOT / "protocol/fixtures/ack.json").read_text())
    assert ack["accepted"] is True
    assert ack["instance_id"] == instance
    assert ack["accepted_sequence"] == sequence
    assert timestamp.utcoffset().total_seconds() == 0
    assert filters == (None, None)
    assert selection.status == "selected"
    assert selection.matching_stream_count == 1
    assert selection.entry.player_id
    assert selection.entry.media_id == "song-1"
    assert ack["missing_artwork"] == [selection.entry.cover_art]


def test_component_versions_match():
    project = tomllib.loads((ROOT / "pyproject.toml").read_text())
    ha = json.loads((ROOT / "custom_components/navidrome_nowplaying/manifest.json").read_text())
    plugin = json.loads((ROOT / "navidrome-plugin/manifest.json").read_text())
    release = json.loads((ROOT / ".release-please-manifest.json").read_text())
    assert project["project"]["version"] == ha["version"] == plugin["version"] == release["."]


def test_release_please_updates_both_component_manifests():
    config = json.loads((ROOT / "release-please-config.json").read_text())
    assert config["release-type"] == "python"
    assert config["include-component-in-tag"] is False
    assert config["include-v-in-tag"] is True
    assert set(config["packages"]) == {"."}
    assert config["packages"]["."]["extra-files"] == [
        {
            "type": "json",
            "path": "custom_components/navidrome_nowplaying/manifest.json",
            "jsonpath": "$.version",
        },
        {
            "type": "json",
            "path": "navidrome-plugin/manifest.json",
            "jsonpath": "$.version",
        },
    ]
