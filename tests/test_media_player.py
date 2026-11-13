"""Tests for read-only entity state and proxy artwork behavior."""

from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime
from unittest.mock import Mock

import pytest
from homeassistant.components.media_player.const import MediaPlayerState

from custom_components.navidrome_nowplaying.coordinator import (
    NowPlayingSnapshot,
    Selection,
    SelectionStatus,
)
from custom_components.navidrome_nowplaying.media_player import NavidromeNowPlayingMediaPlayer
from custom_components.navidrome_nowplaying.models import NowPlayingEntry


def _entry(cover_art: str | None = "cover-1") -> NowPlayingEntry:
    return NowPlayingEntry(
        media_id="song-1",
        title="A Song",
        artist="An Artist",
        album="An Album",
        duration=180,
        cover_art=cover_art,
        username="listener",
        player_id="response-order-id",
        player_name="Kitchen",
        state="playing",
        position_ms=2_500,
        playback_rate=1,
    )


def _entity(snapshot: NowPlayingSnapshot):
    coordinator = type("Coordinator", (), {})()
    coordinator.data = snapshot
    coordinator.image = Mock(return_value=None)
    entity = object.__new__(NavidromeNowPlayingMediaPlayer)
    entity.coordinator = coordinator
    return entity, coordinator


@pytest.mark.parametrize(
    ("navidrome_state", "expected"),
    [
        ("playing", MediaPlayerState.PLAYING),
        ("paused", MediaPlayerState.PAUSED),
        ("starting", MediaPlayerState.BUFFERING),
        ("stopped", MediaPlayerState.IDLE),
        (None, MediaPlayerState.IDLE),
    ],
)
def test_direct_state_mapping(navidrome_state: str | None, expected: MediaPlayerState) -> None:
    selected = replace(_entry(), state=navidrome_state)
    entity, _ = _entity(
        NowPlayingSnapshot(
            datetime(2026, 1, 1, tzinfo=UTC),
            Selection(SelectionStatus.SELECTED, selected, 1),
        )
    )

    assert entity.state is expected


def test_selected_mapping_and_idle_clearing() -> None:
    selected = _entry()
    entity, coordinator = _entity(
        NowPlayingSnapshot(
            datetime(2026, 1, 1, tzinfo=UTC),
            Selection(SelectionStatus.SELECTED, selected, 1),
        )
    )
    assert entity.state is MediaPlayerState.PLAYING
    assert entity.media_title == "A Song"
    assert entity.media_position == 2.5
    assert entity.media_position_updated_at == datetime(2026, 1, 1, tzinfo=UTC)
    assert entity.app_name == "Kitchen"
    assert entity.extra_state_attributes["player_id"] == "response-order-id"
    assert entity.media_image_url is None
    assert len(entity.media_image_hash) == 16

    coordinator.data = NowPlayingSnapshot(
        datetime(2026, 1, 1, tzinfo=UTC),
        Selection(SelectionStatus.AMBIGUOUS, None, 2),
    )
    assert entity.state is MediaPlayerState.IDLE
    assert entity.media_title is None
    assert entity.media_content_id is None
    assert entity.media_image_hash is None
    assert entity.extra_state_attributes == {
        "selection_status": "ambiguous",
        "matching_stream_count": 2,
    }


@pytest.mark.asyncio
async def test_artwork_uses_cache_only() -> None:
    selected = _entry()
    entity, coordinator = _entity(
        NowPlayingSnapshot(datetime.now(UTC), Selection(SelectionStatus.SELECTED, selected, 1))
    )
    assert await entity.async_get_media_image() == (None, None)
    coordinator.image.return_value = (b"jpg", "image/jpeg", "digest")
    assert await entity.async_get_media_image() == (b"jpg", "image/jpeg")
    assert entity.media_image_hash == "digest"
    coordinator.image.assert_called_with("cover-1")
    coordinator.data = None
    assert await entity.async_get_media_image() == (None, None)
