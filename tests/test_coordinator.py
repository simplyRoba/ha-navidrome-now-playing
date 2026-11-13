"""Immutable source-selected DTOs have no HA selection policy."""

from dataclasses import FrozenInstanceError
from datetime import UTC, datetime

import pytest

from custom_components.navidrome_nowplaying.coordinator import (
    NowPlayingSnapshot,
    Selection,
    SelectionStatus,
)
from custom_components.navidrome_nowplaying.models import parse_now_playing_entry


@pytest.mark.parametrize("status,count", [("selected", 7), ("no_match", 0), ("ambiguous", 3)])
def test_source_selection_is_immutable(status, count):
    entry = parse_now_playing_entry({"playerId": "stable-phone", "state": "paused"})
    selection = Selection(SelectionStatus(status), entry if status == "selected" else None, count)
    snapshot = NowPlayingSnapshot(datetime.now(UTC), selection)
    assert snapshot.selection is selection
    assert selection.matching_stream_count == count
    with pytest.raises(FrozenInstanceError):
        selection.matching_stream_count = 1
    with pytest.raises(FrozenInstanceError):
        snapshot.selection = Selection(SelectionStatus.NO_MATCH, None, 0)
