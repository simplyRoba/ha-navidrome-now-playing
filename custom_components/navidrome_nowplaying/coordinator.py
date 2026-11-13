"""Immutable DTOs for Navidrome's authoritative selection."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from enum import StrEnum

from .models import NowPlayingEntry


class SelectionStatus(StrEnum):
    """Why a stream was (or was not) selected."""

    SELECTED = "selected"
    NO_MATCH = "no_match"
    AMBIGUOUS = "ambiguous"


@dataclass(frozen=True, slots=True)
class Selection:
    """The selection result provided by the source."""

    status: SelectionStatus
    entry: NowPlayingEntry | None
    matching_stream_count: int


@dataclass(frozen=True, slots=True)
class NowPlayingSnapshot:
    """An immutable accepted snapshot and its selection."""

    polled_at: datetime
    selection: Selection
