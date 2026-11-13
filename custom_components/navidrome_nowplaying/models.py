"""Neutral now-playing models, parsing and credential-free cache keys."""

from __future__ import annotations

from dataclasses import dataclass
from hashlib import sha256
from math import isfinite
from urllib.parse import urlsplit, urlunsplit


@dataclass(frozen=True, slots=True)
class NowPlayingEntry:
    """A defensively parsed OpenSubsonic now-playing stream."""

    media_id: str | None
    title: str | None
    artist: str | None
    album: str | None
    duration: int | None
    cover_art: str | None
    username: str | None
    player_id: str | None
    player_name: str | None
    state: str | None
    position_ms: int | None
    playback_rate: float | None


def normalize_base_url(value: str) -> str:
    """Validate a user URL while preserving a reverse-proxy path prefix."""
    value = value.strip()
    parsed = urlsplit(value)
    try:
        port = parsed.port  # Access validates the optional port range and syntax.
    except ValueError as err:
        raise ValueError("invalid base URL") from err
    if (
        parsed.scheme not in {"http", "https"}
        or port == 0
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("invalid base URL")

    # urlunsplit keeps an IPv6 port/netloc exactly as entered and drops a final /.
    path = parsed.path.rstrip("/")
    return urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))


def image_cache_hash(base_url: str, cover_art: str) -> str:
    """Return a credential-free hash for Home Assistant's image proxy cache."""
    return sha256(f"{base_url}\x00{cover_art}".encode()).hexdigest()[:16]


def _optional_string(value: object) -> str | None:
    return value if isinstance(value, str) and value else None


def _optional_identifier(value: object) -> str | None:
    """Return a string identifier, accepting Navidrome's numeric player IDs."""
    if isinstance(value, str) and value:
        return value
    if isinstance(value, int) and not isinstance(value, bool):
        return str(value)
    return None


def _optional_int(value: object) -> int | None:
    if isinstance(value, bool):
        return None
    try:
        result = int(value)  # OpenSubsonic commonly serializes these as strings.
    except (TypeError, ValueError):
        return None
    return result if result >= 0 else None


def _optional_float(value: object) -> float | None:
    if isinstance(value, bool):
        return None
    try:
        result = float(value)
    except (TypeError, ValueError):
        return None
    return result if result >= 0 and isfinite(result) else None


def parse_now_playing_entry(value: object) -> NowPlayingEntry | None:
    """Parse one entry, returning None only when its structure is invalid."""
    if not isinstance(value, dict):
        return None
    return NowPlayingEntry(
        media_id=_optional_string(value.get("id")),
        title=_optional_string(value.get("title")),
        artist=_optional_string(value.get("artist")),
        album=_optional_string(value.get("album")),
        duration=_optional_int(value.get("duration")),
        cover_art=_optional_string(value.get("coverArt")),
        username=_optional_string(value.get("username")),
        # Protocol v2 carries the stable source player identity.
        player_id=_optional_identifier(value.get("playerId")),
        player_name=_optional_string(value.get("playerName")),
        state=_optional_string(value.get("state")),
        position_ms=_optional_int(value.get("positionMs")),
        playback_rate=_optional_float(value.get("playbackRate")),
    )
