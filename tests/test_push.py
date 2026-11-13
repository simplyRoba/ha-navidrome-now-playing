"""Exercise push through the real HA HTTP server, entity and authenticated proxy."""

import asyncio
from datetime import timedelta
from hashlib import sha256
from unittest.mock import patch

import pytest
from homeassistant.config_entries import SOURCE_RECONFIGURE
from homeassistant.helpers import entity_registry as er
from homeassistant.util import dt as dt_util
from pytest_homeassistant_custom_component.common import MockConfigEntry, async_fire_time_changed

from custom_components.navidrome_nowplaying.const import DOMAIN
from custom_components.navidrome_nowplaying.media_player import NavidromeNowPlayingMediaPlayer
from custom_components.navidrome_nowplaying.push import RECEIVERS, read_body, validate_snapshot

URL = "/api/navidrome_nowplaying/receiver"
HEADERS = {"Authorization": "Bearer dedicated-secret"}
PNG = b"\x89PNG\r\n\x1a\nimage-bytes"


def payload(sequence=1, instance="instance-a", selected=True, status=None, count=None):
    stream = (
        {
            "id": "song",
            "title": "Title",
            "artist": "Artist",
            "album": "Album",
            "coverArt": "cover",
            "username": "listener",
            "playerId": "stable-phone",
            "playerName": "Kitchen",
            "duration": 180,
            "positionMs": 45000,
            "playbackRate": 1,
            "state": "playing",
        }
        if selected is True
        else selected
        if isinstance(selected, dict)
        else None
    )
    return {
        "version": 2,
        "instance_id": instance,
        "sequence": sequence,
        "snapshot_at": dt_util.utcnow().isoformat(),
        "selected_stream": stream,
        "selection_status": status or ("selected" if stream else "no_match"),
        "matching_stream_count": count if count is not None else (1 if stream else 0),
        "filters": {"playback_username": "listener", "player_name": "Kitchen"},
    }


@pytest.fixture
async def push_entry(hass, enable_custom_integrations):
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={
            "transport": "push",
            "receiver_id": "receiver",
            "secret": "dedicated-secret",
            "ha_base_url": "http://ha.example",
            "push_url": "http://ha.example" + URL,
            "playback_username": "listener",
            "player_name": "Kitchen",
        },
    )
    entry.add_to_hass(hass)
    with patch("aiohttp.ClientSession.get", side_effect=AssertionError("No outbound API")):
        assert await hass.config_entries.async_setup(entry.entry_id)
        await hass.async_block_till_done()
    yield entry
    if entry.entry_id in hass.config_entries.async_entry_ids():
        await hass.config_entries.async_unload(entry.entry_id)


async def test_push_artwork_proxy_and_lifecycle(hass, push_entry, hass_client_no_auth, hass_client):
    client = await hass_client_no_auth()
    auth_client = await hass_client()
    coordinator = push_entry.runtime_data
    assert coordinator.update_interval is None
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    snapshot = payload()
    response = await client.post(URL, headers=HEADERS, json=snapshot)
    assert response.status == 200
    assert await response.json() == {
        "accepted": True,
        "instance_id": "instance-a",
        "accepted_sequence": 1,
        "missing_artwork": ["cover"],
    }
    await hass.async_block_till_done()
    state = hass.states.get("media_player.navidrome")
    assert state.state == "playing"
    assert state.attributes["media_title"] == "Title"
    assert state.attributes["media_position"] == 45
    assert state.attributes["media_position_updated_at"].isoformat() == snapshot["snapshot_at"]
    picture_before = state.attributes["entity_picture"]
    entity = NavidromeNowPlayingMediaPlayer(coordinator, push_entry)
    pending_hash = entity.media_image_hash
    assert await entity.async_get_media_image() == (None, None)
    response = await client.post(
        URL + "/artwork",
        headers={
            **HEADERS,
            "X-Navidrome-Artwork-ID": "cover",
            "Content-Type": "image/png",
        },
        data=PNG,
    )
    assert response.status == 200
    await hass.async_block_till_done()
    assert entity.media_image_hash == sha256(PNG).hexdigest()
    assert pending_hash != entity.media_image_hash
    picture = hass.states.get("media_player.navidrome").attributes["entity_picture"]
    assert picture != picture_before
    # Proxy is HA's normal media endpoint, no upstream URL or pairing secret.
    assert "dedicated-secret" not in picture
    assert await entity.async_get_media_image() == (PNG, "image/png")
    response = await auth_client.get(picture)
    assert response.status == 200
    assert await response.read() == PNG
    assert response.content_type == "image/png"
    # Same coverArt ID with new content invalidates the proxy hash.
    changed = PNG + b"changed"
    response = await client.post(
        URL + "/artwork",
        headers={
            **HEADERS,
            "X-Navidrome-Artwork-ID": "cover",
            "Content-Type": "image/png",
        },
        data=changed,
    )
    assert response.status == 200
    assert entity.media_image_hash == sha256(changed).hexdigest()
    response = await client.post(URL, headers=HEADERS, json=snapshot)
    assert (await response.json())["missing_artwork"] == []
    coordinator.artwork.clear()
    response = await client.post(URL, headers=HEADERS, json=snapshot)
    assert (await response.json())["missing_artwork"] == ["cover"]
    assert await hass.config_entries.async_unload(push_entry.entry_id)
    assert "receiver" not in hass.data[RECEIVERS]
    assert not coordinator.artwork
    response = await client.post(URL, headers=HEADERS, json=snapshot)
    assert response.status == 401
    assert await hass.config_entries.async_setup(push_entry.entry_id)
    assert push_entry.runtime_data is not coordinator
    assert hass.states.get("media_player.navidrome").state == "unavailable"


async def test_auth_validation_and_sizes(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    for headers in (
        {},
        {"Authorization": "Bearer wrong"},
        {"Authorization": "Basic dedicated-secret"},
    ):
        response = await client.post(URL, headers=headers, data=b"not json")
        assert response.status == 401  # Authenticate before decoding anything.
    response = await client.post(
        "/api/navidrome_nowplaying/unknown", headers=HEADERS, json=payload()
    )
    assert response.status == 401
    response = await client.post(URL, headers=HEADERS, data=b"{}")
    assert response.status == 415
    response = await client.post(
        URL, headers={**HEADERS, "Content-Type": "application/json"}, data=b"{bad"
    )
    assert response.status == 400
    response = await client.post(
        URL, headers={**HEADERS, "Content-Type": "application/json"}, data=b"x" * (1024 * 1024 + 1)
    )
    assert response.status == 413

    async def chunks():
        for _ in range(17):
            yield b"x" * (64 * 1024)

    response = await client.post(
        URL, headers={**HEADERS, "Content-Type": "application/json"}, data=chunks()
    )
    assert response.status == 413
    for bad in (
        {},
        [],
        {**payload(), "sequence": True},
        {**payload(), "matching_stream_count": 129},
    ):
        response = await client.post(URL, headers=HEADERS, json=bad)
        assert response.status == 400
    assert not push_entry.runtime_data.last_update_success


async def test_ordering_restart_and_timeout(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    coordinator = push_entry.runtime_data
    first = payload(selected=False)
    assert (await client.post(URL, headers=HEADERS, json=first)).status == 200
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "idle"
    assert coordinator.sequence == 1
    # An old cached snapshot is valid, even when its age exceeds the timeout.
    second = payload(2)
    second["snapshot_at"] = (dt_util.utcnow() - timedelta(days=10)).isoformat()
    await client.post(URL, headers=HEADERS, json=second)
    assert coordinator.data.polled_at.isoformat() == second["snapshot_at"]
    timer = coordinator._cancel_timeout
    await client.post(URL, headers=HEADERS, json=first)
    assert coordinator._cancel_timeout is timer
    assert coordinator.sequence == 2
    assert coordinator.data.selection.entry.media_id == "song"
    await client.post(URL, headers=HEADERS, json=payload(3, "instance-b", False))
    assert coordinator.instance == "instance-a"  # Restart must begin with 1.
    await client.post(URL, headers=HEADERS, json=payload(1, "instance-b", False))
    assert coordinator.instance == "instance-b"
    await client.post(URL, headers=HEADERS, json=payload(9, "instance-a"))
    assert coordinator.instance == "instance-b"
    assert coordinator.sequence == 1
    async_fire_time_changed(hass, dt_util.utcnow() + timedelta(seconds=301))
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    # Late retired traffic cannot revive the entity.
    await client.post(URL, headers=HEADERS, json=payload(10, "instance-a"))
    assert not coordinator.last_update_success
    await client.post(URL, headers=HEADERS, json=payload(2, "instance-b", False))
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "idle"


async def test_artwork_filters_errors_and_eviction(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    coordinator = push_entry.runtime_data
    response = await client.post(URL, headers=HEADERS, json=payload(count=2))
    assert (await response.json())["missing_artwork"] == ["cover"]
    for identifier, mime, body, status in (
        ("other", "image/png", PNG, 409),
        ("cover", "text/plain", PNG, 415),
        ("cover", "image/png", b"", 400),
        ("cover", "image/png", b"invalid", 400),
        ("cover", "image/png", PNG + b"x" * (10 * 1024 * 1024), 413),
    ):
        response = await client.post(
            URL + "/artwork",
            headers={
                **HEADERS,
                "X-Navidrome-Artwork-ID": identifier,
                "Content-Type": mime,
            },
            data=body,
        )
        assert response.status == status
    for mime, body in (
        ("image/jpeg", b"\xff\xd8\xffbytes"),
        ("image/webp", b"RIFFxxxxWEBPbytes"),
        ("image/gif", b"GIF89abytes"),
    ):
        response = await client.post(
            URL + "/artwork",
            headers={
                **HEADERS,
                "X-Navidrome-Artwork-ID": "cover",
                "Content-Type": mime,
            },
            data=body,
        )
        assert response.status == 200
    await client.post(URL, headers=HEADERS, json=payload(2, selected=False))
    for number in range(20):
        coordinator.cache_image(str(number), PNG, "image/png")
    assert len(coordinator.artwork) == 16
    assert "0" not in coordinator.artwork
    coordinator.image("4")  # LRU touch.
    coordinator.cache_image("new", PNG, "image/png")
    assert "4" in coordinator.artwork and "5" not in coordinator.artwork
    for number in range(3):
        coordinator.cache_image(str(number), b"x" * (8 * 1024 * 1024), "image/png")
    assert coordinator.artwork_bytes <= 20 * 1024 * 1024
    response = await client.post(
        URL + "/artwork",
        headers={
            **HEADERS,
            "X-Navidrome-Artwork-ID": "cover",
            "Content-Type": "image/png",
        },
        data=PNG,
    )
    assert response.status == 409


@pytest.mark.parametrize(
    "field,value",
    [
        ("version", True),
        ("version", 1),
        ("instance_id", ""),
        ("instance_id", "x" * 129),
        ("sequence", 0),
        ("sequence", 1.1),
        ("snapshot_at", "2025-01-01T00:00:00"),
        ("snapshot_at", "2025-01-01T00:00:00+01:00"),
        ("snapshot_at", "bad"),
        ("snapshot_at", "2100-01-01T00:00:00Z"),
        ("selected_stream", []),
        ("selected_stream", None),
        ("selection_status", "unknown"),
        ("matching_stream_count", True),
        ("matching_stream_count", -1),
        ("matching_stream_count", 129),
        ("filters", None),
        ("filters", {}),
        ("filters", {"playback_username": 3, "player_name": None}),
    ],
)
def test_invalid_snapshot_fields(field, value):
    with pytest.raises(ValueError):
        validate_snapshot({**payload(), field: value})


@pytest.mark.parametrize(
    "field,value",
    [
        ("title", 123),
        ("title", "x" * 4097),
        ("state", "unknown"),
        ("playerId", True),
        ("playerId", []),
        ("duration", -1),
        ("duration", 0.1),
        ("positionMs", True),
        ("playbackRate", float("nan")),
        ("playbackRate", float("inf")),
    ],
)
def test_invalid_stream_fields(field, value):
    snapshot = payload()
    snapshot["selected_stream"][field] = value
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


@pytest.mark.parametrize("selected", [True, False], ids=["playing", "idle"])
async def test_push_timeout_is_five_minutes(
    hass, push_entry, hass_client_no_auth, freezer, selected
):
    client = await hass_client_no_auth()
    start = dt_util.utcnow()
    response = await client.post(URL, headers=HEADERS, json=payload(selected=selected))
    assert response.status == 200
    await hass.async_block_till_done()
    expected = "playing" if selected else "idle"
    for elapsed in (91, 299):
        freezer.move_to(start + timedelta(seconds=elapsed))
        async_fire_time_changed(hass, dt_util.utcnow())
        await hass.async_block_till_done()
        assert push_entry.runtime_data.last_update_success
        assert hass.states.get("media_player.navidrome").state == expected
    freezer.move_to(start + timedelta(seconds=300))
    async_fire_time_changed(hass, dt_util.utcnow())
    await hass.async_block_till_done()
    assert not push_entry.runtime_data.last_update_success
    assert hass.states.get("media_player.navidrome").state == "unavailable"


async def test_heartbeats_extend_five_minute_timeout(
    hass, push_entry, hass_client_no_auth, freezer
):
    client = await hass_client_no_auth()
    start = dt_util.utcnow()
    snapshot = payload(2)
    assert (await client.post(URL, headers=HEADERS, json=snapshot)).status == 200
    await hass.async_block_till_done()
    sample = push_entry.runtime_data.data.polled_at
    for elapsed in (90, 180, 270):
        freezer.move_to(start + timedelta(seconds=elapsed))
        async_fire_time_changed(hass, dt_util.utcnow())
        await hass.async_block_till_done()
        assert hass.states.get("media_player.navidrome").state == "playing"
        response = await client.post(URL, headers=HEADERS, json=snapshot)
        assert response.status == 200
        assert (await response.json())["accepted"]
        assert push_entry.runtime_data.data.polled_at == sample
    # Rejected old metadata must not extend the final accepted heartbeat's deadline.
    freezer.move_to(start + timedelta(seconds=500))
    response = await client.post(URL, headers=HEADERS, json=payload(1, selected=False))
    assert response.status == 200
    assert not (await response.json())["accepted"]
    freezer.move_to(start + timedelta(seconds=569))
    async_fire_time_changed(hass, dt_util.utcnow())
    await hass.async_block_till_done()
    assert push_entry.runtime_data.last_update_success
    freezer.move_to(start + timedelta(seconds=570))
    async_fire_time_changed(hass, dt_util.utcnow())
    await hass.async_block_till_done()
    assert not push_entry.runtime_data.last_update_success
    assert hass.states.get("media_player.navidrome").state == "unavailable"


async def test_delayed_upload_and_timeout_do_not_revive(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    coordinator = push_entry.runtime_data
    await client.post(URL, headers=HEADERS, json=payload())
    started, release = asyncio.Event(), asyncio.Event()

    async def delayed_read(request, limit):
        if request.path.endswith("/artwork"):
            started.set()
            await release.wait()
        return await read_body(request, limit)

    with patch("custom_components.navidrome_nowplaying.push.read_body", new=delayed_read):
        upload = asyncio.create_task(
            client.post(
                URL + "/artwork",
                headers={
                    **HEADERS,
                    "X-Navidrome-Artwork-ID": "cover",
                    "Content-Type": "image/png",
                },
                data=PNG,
            )
        )
        await started.wait()
        await client.post(URL, headers=HEADERS, json=payload(2, selected=False))
        release.set()
        assert (await upload).status == 409
    assert not coordinator.artwork
    await client.post(URL, headers=HEADERS, json=payload(3))
    async_fire_time_changed(hass, dt_util.utcnow() + timedelta(seconds=301))
    await hass.async_block_till_done()
    response = await client.post(
        URL + "/artwork",
        headers={
            **HEADERS,
            "X-Navidrome-Artwork-ID": "cover",
            "Content-Type": "image/png",
        },
        data=PNG,
    )
    assert response.status == 200
    await hass.async_block_till_done()
    assert not coordinator.last_update_success
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    # A duplicate of the current snapshot is a valid heartbeat, not fresh position data.
    snapshot = payload(3)
    instant = coordinator.data.polled_at
    await client.post(URL, headers=HEADERS, json=snapshot)
    assert coordinator.data.polled_at == instant
    assert coordinator.last_update_success


async def test_real_rotation_reload_preserves_entity(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    await client.post(URL, headers=HEADERS, json=payload())
    registry = er.async_get(hass)
    unique_id = registry.async_get("media_player.navidrome").unique_id
    result = await hass.config_entries.flow.async_init(
        DOMAIN,
        context={"source": SOURCE_RECONFIGURE, "entry_id": push_entry.entry_id},
        data={"ha_base_url": "http://ha.example/prefix", "rotate_secret": True},
    )
    new_secret = result["description_placeholders"]["secret"]
    assert new_secret != push_entry.data["secret"]
    # Pairing form itself does not rotate or unload the active receiver.
    assert (await client.post(URL, headers=HEADERS, json=payload(2))).status == 200
    await hass.config_entries.flow.async_configure(result["flow_id"], {})
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    assert registry.async_get("media_player.navidrome").unique_id == unique_id
    assert push_entry.data["receiver_id"] == "receiver"
    assert (await client.post(URL, headers=HEADERS, json=payload(3))).status == 401
    response = await client.post(
        URL, headers={"Authorization": f"Bearer {new_secret}"}, json=payload(instance="new-plugin")
    )
    assert response.status == 200
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "playing"


@pytest.mark.usefixtures("enable_custom_integrations")
async def test_legacy_to_push_preserves_registered_entity(hass, hass_client_no_auth):
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={
            "base_url": "https://music.example",
            "username": "account",
            "password": "private",
            "playback_username": "listener",
            "player_name": "Kitchen",
            "poll_interval": 5,
            "verify_ssl": True,
        },
    )
    entry.add_to_hass(hass)
    unique_id = f"{entry.entry_id}_now_playing"
    er.async_get(hass).async_get_or_create(
        "media_player",
        DOMAIN,
        unique_id,
        suggested_object_id="navidrome",
        config_entry=entry,
    )
    assert not await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    result = await hass.config_entries.flow.async_init(
        DOMAIN,
        context={"source": SOURCE_RECONFIGURE, "entry_id": entry.entry_id},
        data={"ha_base_url": "http://ha.example"},
    )
    details = result["description_placeholders"]
    await hass.config_entries.flow.async_configure(result["flow_id"], {})
    await hass.async_block_till_done()
    assert er.async_get(hass).async_get("media_player.navidrome").unique_id == unique_id
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    assert all(
        key not in entry.data
        for key in ("base_url", "username", "password", "verify_ssl", "poll_interval")
    )
    assert entry.data["playback_username"] == "listener"
    client = await hass_client_no_auth()
    response = await client.post(
        details["push_url"].removeprefix("http://ha.example"),
        headers={"Authorization": f"Bearer {details['secret']}"},
        json=payload(),
    )
    assert response.status == 200
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "playing"
    assert "account" not in str(hass.states.get("media_player.navidrome").attributes)
    assert await hass.config_entries.async_unload(entry.entry_id)


async def test_restart_adopts_running_sender(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    response = await client.post(URL, headers=HEADERS, json=payload(42))
    assert (await response.json())["accepted"] is True
    assert push_entry.runtime_data.sequence == 42
    assert await hass.config_entries.async_reload(push_entry.entry_id)
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    response = await client.post(URL, headers=HEADERS, json=payload(43))
    ack = await response.json()
    assert ack["accepted"] is True
    assert ack["instance_id"] == "instance-a"
    assert ack["accepted_sequence"] == 43
    await hass.async_block_till_done()
    assert hass.states.get("media_player.navidrome").state == "playing"


async def test_retired_instances_never_become_valid_again(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    for number in range(19):
        response = await client.post(
            URL, headers=HEADERS, json=payload(instance=f"instance-{number}")
        )
        assert (await response.json())["accepted"] is True
    coordinator = push_entry.runtime_data
    response = await client.post(URL, headers=HEADERS, json=payload(instance="instance-0"))
    ack = await response.json()
    assert ack["accepted"] is False
    assert ack["instance_id"] == "instance-18"
    assert coordinator.instance == "instance-18"
    # At the bounded limit refuse new identities rather than evict replay guards.
    coordinator.retired.update(f"retired-{n}" for n in range(1024 - len(coordinator.retired)))
    response = await client.post(URL, headers=HEADERS, json=payload(instance="excess"))
    assert (await response.json())["accepted"] is False
    assert len(coordinator.retired) == 1024


async def test_only_selected_artwork_is_requested(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    selected = {**payload()["selected_stream"], "coverArt": "zzz-selected", "state": "paused"}
    response = await client.post(URL, headers=HEADERS, json=payload(selected=selected, count=34))
    assert (await response.json())["missing_artwork"] == ["zzz-selected"]
    assert push_entry.runtime_data.wanted == {"zzz-selected"}
    # Count/byte pressure from previous selections must not request that history.
    for number in range(20):
        push_entry.runtime_data.cache_image(str(number), PNG, "image/png")
    for number in range(3):
        push_entry.runtime_data.cache_image(str(number), b"x" * (8 * 1024 * 1024), "image/png")
    response = await client.post(URL, headers=HEADERS, json=payload(2, selected=selected, count=34))
    assert (await response.json())["missing_artwork"] == ["zzz-selected"]
    response = await client.post(
        URL + "/artwork",
        headers={**HEADERS, "X-Navidrome-Artwork-ID": "zzz-selected", "Content-Type": "image/png"},
        data=PNG,
    )
    assert response.status == 200
    response = await client.post(URL, headers=HEADERS, json=payload(3, selected=selected, count=34))
    assert (await response.json())["missing_artwork"] == []
    # Ambiguous state displays no image, so no artwork should be requested.
    response = await client.post(
        URL, headers=HEADERS, json=payload(4, selected=False, status="ambiguous", count=2)
    )
    assert (await response.json())["missing_artwork"] == []


async def test_upload_survives_unrelated_heartbeat(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    await client.post(URL, headers=HEADERS, json=payload())
    started, release = asyncio.Event(), asyncio.Event()

    async def delayed_read(request, limit):
        if request.path.endswith("/artwork"):
            started.set()
            await release.wait()
        return await read_body(request, limit)

    with patch("custom_components.navidrome_nowplaying.push.read_body", new=delayed_read):
        upload = asyncio.create_task(
            client.post(
                URL + "/artwork",
                headers={**HEADERS, "X-Navidrome-Artwork-ID": "cover", "Content-Type": "image/png"},
                data=PNG,
            )
        )
        await started.wait()
        await client.post(URL, headers=HEADERS, json=payload(2))
        release.set()
        assert (await upload).status == 200
    assert push_entry.runtime_data.image("cover")[:2] == (PNG, "image/png")


async def test_delayed_requests_cannot_cross_secret_rotation(hass, push_entry, hass_client_no_auth):
    client = await hass_client_no_auth()
    await client.post(URL, headers=HEADERS, json=payload())
    started, release = asyncio.Event(), asyncio.Event()
    readers = 0

    async def delayed_read(request, limit):
        nonlocal readers
        readers += 1
        if readers == 2:
            started.set()
        await release.wait()
        return await read_body(request, limit)

    with patch("custom_components.navidrome_nowplaying.push.read_body", new=delayed_read):
        snapshot_request = asyncio.create_task(client.post(URL, headers=HEADERS, json=payload(2)))
        image_request = asyncio.create_task(
            client.post(
                URL + "/artwork",
                headers={**HEADERS, "X-Navidrome-Artwork-ID": "cover", "Content-Type": "image/png"},
                data=PNG,
            )
        )
        await started.wait()
        result = await hass.config_entries.flow.async_init(
            DOMAIN,
            context={"source": SOURCE_RECONFIGURE, "entry_id": push_entry.entry_id},
            data={"ha_base_url": "http://ha.example", "rotate_secret": True},
        )
        await hass.config_entries.flow.async_configure(result["flow_id"], {})
        await hass.async_block_till_done()
        release.set()
        assert (await snapshot_request).status == 401
        assert (await image_request).status == 409
    assert not push_entry.runtime_data.last_update_success
    assert not push_entry.runtime_data.artwork
