"""Tests for defensive stream parsing and neutral URL/cache helpers."""

import pytest

from custom_components.navidrome_nowplaying.models import (
    image_cache_hash,
    normalize_base_url,
    parse_now_playing_entry,
)


def test_parse_optional_fields():
    entry = parse_now_playing_entry(
        {
            "id": "song",
            "title": "Title",
            "artist": "Artist",
            "album": "Album",
            "duration": "180",
            "coverArt": "cover",
            "username": "listener",
            "playerId": 2,
            "playerName": "Kitchen",
            "state": "playing",
            "positionMs": "2500",
            "playbackRate": "1.5",
        }
    )
    assert entry.media_id == "song"
    assert entry.duration == 180
    assert entry.player_id == "2"
    assert entry.position_ms == 2500
    assert entry.playback_rate == 1.5
    assert parse_now_playing_entry("invalid") is None


@pytest.mark.parametrize("value", [None, True, False, -1, "invalid", ""])
def test_invalid_optional_values(value):
    entry = parse_now_playing_entry(
        {
            "duration": value,
            "positionMs": value,
            "playbackRate": value,
        }
    )
    assert entry.duration is None
    assert entry.position_ms is None
    assert entry.playback_rate is None


@pytest.mark.parametrize("value", [float("nan"), float("inf"), "nan", "inf"])
def test_nonfinite_rate(value):
    assert parse_now_playing_entry({"playbackRate": value}).playback_rate is None


def test_identifiers_and_empty_fields():
    assert parse_now_playing_entry({"playerId": "stable"}).player_id == "stable"
    assert parse_now_playing_entry({"playerId": True}).player_id is None
    assert parse_now_playing_entry({"title": "", "artist": 12}).title is None


def test_url_and_cache_helpers():
    assert normalize_base_url(" https://music.example/navidrome/ ") == (
        "https://music.example/navidrome"
    )
    assert normalize_base_url("http://[::1]:8123/") == "http://[::1]:8123"
    assert image_cache_hash("push", "cover") == image_cache_hash("push", "cover")
    assert image_cache_hash("push", "cover") != image_cache_hash("push", "other")


@pytest.mark.parametrize(
    "url",
    [
        "music.example",
        "ftp://music.example",
        "https://user@music.example",
        "https://u:p@music.example",
        "https://music.example/?x=1",
        "https://music.example/#fragment",
        "https://music.example:70000",
        "https://music.example:0",
        "https://music.example:invalid",
    ],
)
def test_invalid_urls(url):
    with pytest.raises(ValueError):
        normalize_base_url(url)
