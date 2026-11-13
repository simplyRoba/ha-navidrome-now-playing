"""Read-only native media player for the selected Navidrome stream."""

from __future__ import annotations

from datetime import datetime
from typing import Any

from homeassistant.components.media_player import (
    MediaPlayerEntity,
    MediaPlayerEntityFeature,
    MediaType,
)
from homeassistant.components.media_player.const import MediaPlayerState
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .coordinator import SelectionStatus
from .models import NowPlayingEntry, image_cache_hash
from .push import PushCoordinator

type NowPlayingCoordinator = PushCoordinator


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry[NowPlayingCoordinator],
    async_add_entities: AddEntitiesCallback,
) -> None:
    """Create exactly one entity for each integration entry."""
    async_add_entities([NavidromeNowPlayingMediaPlayer(entry.runtime_data, entry)])


class NavidromeNowPlayingMediaPlayer(CoordinatorEntity[NowPlayingCoordinator], MediaPlayerEntity):
    """Project one unambiguous now-playing stream without playback controls."""

    _attr_name = "Navidrome"
    _attr_supported_features = MediaPlayerEntityFeature(0)
    _attr_media_image_remotely_accessible = False

    def __init__(
        self,
        coordinator: NowPlayingCoordinator,
        entry: ConfigEntry[NowPlayingCoordinator],
    ) -> None:
        super().__init__(coordinator)
        self._attr_unique_id = f"{entry.entry_id}_now_playing"

    @property
    def _selected(self) -> NowPlayingEntry | None:
        snapshot = self.coordinator.data
        return snapshot.selection.entry if snapshot else None

    @property
    def _selection_status(self) -> SelectionStatus:
        snapshot = self.coordinator.data
        return snapshot.selection.status if snapshot else SelectionStatus.NO_MATCH

    @property
    def state(self) -> MediaPlayerState:
        entry = self._selected
        if entry is None:
            return MediaPlayerState.IDLE
        return {
            "playing": MediaPlayerState.PLAYING,
            "paused": MediaPlayerState.PAUSED,
            "stopped": MediaPlayerState.IDLE,
            "idle": MediaPlayerState.IDLE,
            "starting": MediaPlayerState.BUFFERING,
        }.get((entry.state or "").lower(), MediaPlayerState.IDLE)

    @property
    def media_title(self) -> str | None:
        return self._selected.title if self._selected else None

    @property
    def media_artist(self) -> str | None:
        return self._selected.artist if self._selected else None

    @property
    def media_album_name(self) -> str | None:
        return self._selected.album if self._selected else None

    @property
    def media_duration(self) -> int | None:
        return self._selected.duration if self._selected else None

    @property
    def media_position(self) -> float | None:
        entry = self._selected
        return entry.position_ms / 1000 if entry and entry.position_ms is not None else None

    @property
    def media_position_updated_at(self) -> datetime | None:
        snapshot = self.coordinator.data
        entry = self._selected
        if entry is None or entry.position_ms is None or snapshot is None:
            return None
        return snapshot.polled_at

    @property
    def media_content_type(self) -> MediaType | None:
        return MediaType.MUSIC if self._selected else None

    @property
    def media_content_id(self) -> str | None:
        return self._selected.media_id if self._selected else None

    @property
    def app_name(self) -> str | None:
        return self._selected.player_name if self._selected else None

    @property
    def media_image_hash(self) -> str | None:
        """Return a cache key that changes with the selected artwork."""
        entry = self._selected
        if entry is None or entry.cover_art is None:
            return None
        image = self.coordinator.image(entry.cover_art)
        return image[2] if image else image_cache_hash("push-pending", entry.cover_art)

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        snapshot = self.coordinator.data
        selection = snapshot.selection if snapshot else None
        attributes: dict[str, Any] = {
            "selection_status": self._selection_status.value,
            "matching_stream_count": selection.matching_stream_count if selection else 0,
        }
        if (entry := self._selected) is not None:
            attributes.update(
                {
                    "navidrome_username": entry.username,
                    "player_name": entry.player_name,
                    "player_id": entry.player_id,
                }
            )
        return attributes

    async def async_get_media_image(self) -> tuple[bytes | None, str | None]:
        """Supply image bytes only when HA's authenticated proxy asks for them."""
        entry = self._selected
        cover_art = entry.cover_art if entry else None
        if cover_art is None:
            return None, None
        cached = self.coordinator.image(cover_art)
        return (cached[0], cached[1]) if cached else (None, None)
