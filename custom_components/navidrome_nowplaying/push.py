"""Authenticated, bounded push receiver and in-memory artwork cache."""

from __future__ import annotations

import json
import logging
import re
import secrets
from collections import OrderedDict
from datetime import timedelta
from hashlib import sha256
from math import isfinite

from aiohttp import web
from homeassistant.components.http import HomeAssistantView
from homeassistant.core import callback
from homeassistant.helpers.event import async_call_later
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed
from homeassistant.util import dt as dt_util

from .const import (
    CONF_PLAYBACK_USERNAME,
    CONF_PLAYER_NAME,
    DOMAIN,
    MAX_ARTWORK_BYTES,
    MAX_JSON_RESPONSE_BYTES,
    PUSH_TIMEOUT_SECONDS,
)
from .coordinator import NowPlayingSnapshot, Selection, SelectionStatus
from .models import parse_now_playing_entry

URL = "/api/navidrome_nowplaying/{receiver_id}"
RECEIVERS = f"{DOMAIN}_receivers"


def validate_snapshot(value):
    """Validate v2 source selection without selecting or coercing fields."""
    if (
        not isinstance(value, dict)
        or type(value.get("version")) is not int
        or value["version"] != 2
    ):
        raise ValueError("Protocol v2 required; upgrade both components")
    if set(value) != {
        "version",
        "instance_id",
        "sequence",
        "snapshot_at",
        "selection_status",
        "matching_stream_count",
        "selected_stream",
        "filters",
    }:
        raise ValueError
    instance = value.get("instance_id")
    sequence = value.get("sequence")
    if (
        not isinstance(instance, str)
        or "\x00" in instance
        or not 1 <= len(instance.encode("utf-8")) <= 128
    ):
        raise ValueError
    if type(sequence) is not int or not 1 <= sequence <= 2**63 - 1:
        raise ValueError
    timestamp = value.get("snapshot_at")
    if (
        not isinstance(timestamp, str)
        or len(timestamp) > 64
        or not re.fullmatch(
            r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|\+00:00)", timestamp
        )
    ):
        raise ValueError
    instant = dt_util.parse_datetime(timestamp)
    if instant is None or instant.utcoffset() != timedelta(0):
        raise ValueError
    if instant > dt_util.utcnow() + timedelta(minutes=5):
        raise ValueError
    filters = value.get("filters")
    if not isinstance(filters, dict) or set(filters) != {CONF_PLAYBACK_USERNAME, CONF_PLAYER_NAME}:
        raise ValueError
    for field in filters.values():
        if field is not None and not _bounded_string(field):
            raise ValueError
    status = SelectionStatus(value.get("selection_status"))
    count = value.get("matching_stream_count")
    if type(count) is not int or not 0 <= count <= 128:
        raise ValueError
    if "selected_stream" not in value:
        raise ValueError
    stream = value["selected_stream"]
    if status == SelectionStatus.SELECTED:
        if not isinstance(stream, dict) or count < 1:
            raise ValueError
    elif stream is not None or (count != 0 if status == SelectionStatus.NO_MATCH else count < 2):
        raise ValueError
    if stream is not None:
        for key in (
            "id",
            "title",
            "artist",
            "album",
            "coverArt",
            "username",
            "playerName",
            "state",
        ):
            field = stream.get(key)
            if field is not None and not _bounded_string(field):
                raise ValueError
        player_id = stream.get("playerId")
        if not _bounded_string(player_id) or not player_id:
            raise ValueError
        if stream.get("state") not in (None, "playing", "paused", "starting", "stopped", "idle"):
            raise ValueError
        for key in ("duration", "positionMs", "playbackRate"):
            field = stream.get(key)
            if field is not None and (
                type(field) not in (int, float) or not 0 <= field <= 2**53 or not isfinite(field)
            ):
                raise ValueError
        for key in ("duration", "positionMs"):
            field = stream.get(key)
            if field is not None and int(field) != field:
                raise ValueError
    selection = Selection(status, parse_now_playing_entry(stream), count)
    return (
        instance,
        sequence,
        instant,
        selection,
        (filters[CONF_PLAYBACK_USERNAME], filters[CONF_PLAYER_NAME]),
    )


def _bounded_string(value):
    return isinstance(value, str) and "\x00" not in value and len(value.encode("utf-8")) <= 4096


class SourceFiltersMismatch(ValueError):
    """Saved HA filters have not yet been copied to the source."""


class PushCoordinator(DataUpdateCoordinator[NowPlayingSnapshot]):
    """A push-only coordinator: no account credentials or polling client."""

    def __init__(self, hass, entry):
        super().__init__(
            hass,
            logging.getLogger(__name__),
            config_entry=entry,
            name=DOMAIN,
        )
        self.secret = entry.data["secret"]
        self.instance = None
        self.sequence = 0
        self.retired = set()
        self.artwork = OrderedDict()
        self.artwork_bytes = 0
        self.wanted = set()
        self.generation = 0
        self._cancel_timeout = None
        self._entry = entry
        self._legacy_filters = (
            entry.data.get(CONF_PLAYBACK_USERNAME) or None,
            entry.data.get(CONF_PLAYER_NAME) or None,
        )
        self._source_filters_migrated = entry.data.get("source_filters_migrated") is True
        self.last_update_success = False

    @callback
    def close(self):
        """Stop liveness tracking and release cached images on unload."""
        if self._cancel_timeout:
            self._cancel_timeout()
            self._cancel_timeout = None
        self.generation += 1
        self.wanted.clear()
        self.artwork.clear()
        self.artwork_bytes = 0

    @callback
    def _expired(self, _now):
        self._cancel_timeout = None
        self.async_set_update_error(UpdateFailed("Push heartbeat timed out"))

    def accept(self, snapshot):
        """Accept ordered snapshots; stale traffic cannot refresh liveness."""
        instance, sequence, instant, selection, filters = snapshot
        if (
            not self._source_filters_migrated
            and any(self._legacy_filters)
            and filters != self._legacy_filters
        ):
            self.logger.warning(
                "Copy saved playback_username and player_name filters to Navidrome plugin settings"
            )
            raise SourceFiltersMismatch
        if instance != self.instance:
            # A fresh receiver can adopt a running sender after HA reload/restart.
            # Once paired, require a new-instance handshake and never evict retired
            # identities: reject excess transitions rather than permit old replay.
            if instance in self.retired or (
                self.instance is not None and (sequence != 1 or len(self.retired) >= 1024)
            ):
                return False
            if self.instance is not None:
                self.retired.add(self.instance)
            self.instance = instance
            self.sequence = 0
            self.generation += 1
        if sequence < self.sequence:
            return False
        if sequence > self.sequence:
            self.sequence = sequence
            selected_art = selection.entry.cover_art if selection.entry else None
            # Only one image can be displayed. Requesting every matching stream
            # would exceed the cache working set and starve the selected image.
            self.wanted = {selected_art} if selected_art else set()
            self.data = NowPlayingSnapshot(instant, selection)
        if self._cancel_timeout:
            self._cancel_timeout()
        self._cancel_timeout = async_call_later(self.hass, PUSH_TIMEOUT_SECONDS, self._expired)
        self.async_set_updated_data(self.data)
        if not self._source_filters_migrated:
            self._source_filters_migrated = True
            # Persist after the ACK callback, without reloading the live receiver.
            self.hass.loop.call_soon(self._mark_source_filters_migrated)
        return True

    @callback
    def _mark_source_filters_migrated(self):
        self.hass.config_entries.async_update_entry(
            self._entry, data=dict(self._entry.data, source_filters_migrated=True)
        )

    def missing_artwork(self):
        return sorted(self.wanted - self.artwork.keys())[:16]

    def cache_image(self, identifier, body, mime):
        if identifier in self.artwork:
            old = self.artwork.pop(identifier)
            self.artwork_bytes -= len(old[0])
        self.artwork[identifier] = (body, mime, sha256(body).hexdigest())
        self.artwork_bytes += len(body)
        while len(self.artwork) > 16 or self.artwork_bytes > 20 * 1024 * 1024:
            _, old = self.artwork.popitem(last=False)
            self.artwork_bytes -= len(old[0])
        # Artwork is not a heartbeat and must not revive a timed-out receiver.
        if self.last_update_success:
            self.async_set_updated_data(self.data)
        else:
            self.async_update_listeners()

    def image(self, identifier):
        if identifier in self.artwork:
            self.artwork.move_to_end(identifier)
            return self.artwork[identifier]
        return None


async def read_body(request, limit):
    """Enforce size limits even on chunked bodies before allocating the full body."""
    if request.content_length is not None and request.content_length > limit:
        raise web.HTTPRequestEntityTooLarge(max_size=limit, actual_size=request.content_length)
    body = bytearray()
    async for chunk in request.content.iter_chunked(64 * 1024):
        body.extend(chunk)
        if len(body) > limit:
            raise web.HTTPRequestEntityTooLarge(max_size=limit, actual_size=len(body))
    return bytes(body)


class PushView(HomeAssistantView):
    """One global route, active entries resolved before reading any request body."""

    url = URL
    name = f"api:{DOMAIN}:push"
    requires_auth = False

    def __init__(self, receivers):
        self.receivers = receivers

    def authenticate(self, request, receiver_id):
        coordinator = self.receivers.get(receiver_id)
        authorization = request.headers.get("Authorization", "")
        if coordinator is None or not secrets.compare_digest(
            authorization.encode(), f"Bearer {coordinator.secret}".encode()
        ):
            raise web.HTTPUnauthorized
        return coordinator

    async def post(self, request, receiver_id):
        coordinator = self.authenticate(request, receiver_id)
        if request.content_type != "application/json":
            raise web.HTTPUnsupportedMediaType
        body = await read_body(request, MAX_JSON_RESPONSE_BYTES)
        try:
            snapshot = validate_snapshot(json.loads(body))
        except (ValueError, TypeError, OverflowError, RecursionError, UnicodeError) as err:
            raise web.HTTPBadRequest(
                text="Protocol v2 required; upgrade both components"
                if isinstance(err, ValueError) and str(err).startswith("Protocol v2")
                else "Invalid v2 snapshot"
            ) from None
        if self.receivers.get(receiver_id) is not coordinator:
            raise web.HTTPUnauthorized
        try:
            accepted = coordinator.accept(snapshot)
        except SourceFiltersMismatch:
            return self.json(
                {
                    "accepted": False,
                    "instance_id": coordinator.instance,
                    "accepted_sequence": coordinator.sequence,
                    "missing_artwork": [],
                    "error": "source_filters_mismatch: copy saved HA filters to Navidrome settings",
                },
                status_code=409,
            )
        return self.json(
            {
                "accepted": accepted,
                "instance_id": coordinator.instance,
                "accepted_sequence": coordinator.sequence,
                "missing_artwork": coordinator.missing_artwork() if accepted else [],
            }
        )


class ArtworkView(PushView):
    """Raw image upload; only artwork needed by currently matching streams."""

    url = URL + "/artwork"
    name = f"api:{DOMAIN}:artwork"

    async def post(self, request, receiver_id):
        coordinator = self.authenticate(request, receiver_id)
        identifier = request.headers.get("X-Navidrome-Artwork-ID", "")
        if identifier not in coordinator.wanted:
            raise web.HTTPConflict
        mime = request.content_type
        if mime not in {"image/png", "image/jpeg", "image/webp", "image/gif"}:
            raise web.HTTPUnsupportedMediaType
        generation = coordinator.generation
        body = await read_body(request, MAX_ARTWORK_BYTES)
        valid = {
            "image/png": body.startswith(b"\x89PNG\r\n\x1a\n"),
            "image/jpeg": body.startswith(b"\xff\xd8\xff"),
            "image/webp": body.startswith(b"RIFF") and body[8:12] == b"WEBP",
            "image/gif": body.startswith((b"GIF87a", b"GIF89a")),
        }
        if not valid[mime]:
            raise web.HTTPBadRequest
        if (
            self.receivers.get(receiver_id) is not coordinator
            or generation != coordinator.generation
            or identifier not in coordinator.wanted
        ):
            raise web.HTTPConflict
        coordinator.cache_image(identifier, body, mime)
        return self.json({})


def register_views(hass):
    """Register the push routes once, before an HTTP server starts."""
    if RECEIVERS not in hass.data:
        receivers = {}
        hass.http.register_view(PushView(receivers))
        hass.http.register_view(ArtworkView(receivers))
        hass.data[RECEIVERS] = receivers


def register_receiver(hass, receiver_id, coordinator):
    """Keep only active receivers in the global route's dispatch map."""
    register_views(hass)
    hass.data[RECEIVERS][receiver_id] = coordinator
