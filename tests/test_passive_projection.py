"""Server authority and one-time saved-filter handoff safety."""

import pytest
from homeassistant.util import dt as dt_util
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.navidrome_nowplaying.const import DOMAIN
from custom_components.navidrome_nowplaying.media_player import NavidromeNowPlayingMediaPlayer
from custom_components.navidrome_nowplaying.push import (
    PushCoordinator,
    SourceFiltersMismatch,
    validate_snapshot,
)


def frame(sequence=42):
    return {
        "version": 2,
        "instance_id": "running-source",
        "sequence": sequence,
        "snapshot_at": dt_util.utcnow().isoformat(),
        "selection_status": "selected",
        "matching_stream_count": 8,
        "selected_stream": {
            "playerId": "stable-phone",
            "playerName": "Same Name",
            "username": "source-user",
            "title": "audioTitle",
            "state": "paused",
            "duration": 0,
            "positionMs": 0,
            "playbackRate": 0,
            "coverArt": "only-selected",
        },
        "filters": {"playback_username": None, "player_name": None},
    }


def receiver(hass, **extra):
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={
            "transport": "push",
            "secret": "private",
            "receiver_id": "stable-receiver",
            "push_url": "http://ha/api/navidrome_nowplaying/stable-receiver",
            **extra,
        },
    )
    entry.add_to_hass(hass)
    return entry, PushCoordinator(hass, entry)


@pytest.mark.parametrize(
    "filters",
    [
        {"playback_username": None, "player_name": None},
        {"playback_username": "saved", "player_name": "unexpected"},
        {"playback_username": "other", "player_name": None},
    ],
)
async def test_legacy_mismatch_changes_nothing(hass, filters, caplog):
    entry, coordinator = receiver(hass, playback_username="saved")
    original = dict(entry.data)
    snapshot = frame()
    snapshot["filters"] = filters
    with pytest.raises(SourceFiltersMismatch):
        coordinator.accept(validate_snapshot(snapshot))
    await hass.async_block_till_done()
    assert dict(entry.data) == original
    assert coordinator.instance is None
    assert coordinator.sequence == 0
    assert coordinator.data is None
    assert coordinator._cancel_timeout is None
    assert coordinator.generation == 0
    assert not coordinator.wanted
    assert "Copy saved" in caplog.text
    assert "private" not in caplog.text
    assert original["push_url"] not in caplog.text
    coordinator.close()


async def test_matched_handoff_persists_without_reload_then_source_can_change(hass):
    entry, coordinator = receiver(hass, playback_username="saved", player_name="Same Name")
    original = dict(entry.data)
    snapshot = frame()
    snapshot["filters"] = {"playback_username": "saved", "player_name": "Same Name"}
    assert coordinator.accept(validate_snapshot(snapshot))
    timer = coordinator._cancel_timeout
    instant = coordinator.data.polled_at
    await hass.async_block_till_done()
    assert dict(entry.data) == dict(original, source_filters_migrated=True)
    assert coordinator._cancel_timeout is timer
    assert coordinator.data.polled_at == instant
    assert coordinator.sequence == 42
    # HA must project the selected paused player even though its username does
    # not match saved HA filters: the source alone enforces the declared policy.
    assert coordinator.data.selection.entry.username == "source-user"
    assert coordinator.data.selection.matching_stream_count == 8
    snapshot = frame(43)
    snapshot["selected_stream"]["playerName"] = "Other Player"
    assert coordinator.accept(validate_snapshot(snapshot))
    assert coordinator.data.selection.entry.player_name == "Other Player"
    assert dict(entry.data) == dict(original, source_filters_migrated=True)
    coordinator.close()
    # The persistent marker remains authoritative on reload.
    reloaded = PushCoordinator(hass, entry)
    assert reloaded.accept(validate_snapshot(frame(99)))
    await hass.async_block_till_done()
    assert reloaded.sequence == 99
    reloaded.close()


async def test_new_receiver_projects_status_count_and_clears_all_metadata(hass):
    entry, coordinator = receiver(hass)
    assert coordinator.accept(validate_snapshot(frame()))
    entity = NavidromeNowPlayingMediaPlayer(coordinator, entry)
    assert entity.state == "paused"
    assert entity.media_title == "audioTitle"
    assert entity.media_duration == 0
    assert entity.media_position == 0
    assert entity.extra_state_attributes["player_id"] == "stable-phone"
    assert entity.extra_state_attributes["matching_stream_count"] == 8
    assert coordinator.missing_artwork() == ["only-selected"]
    for sequence, status, count in [(43, "ambiguous", 17), (44, "no_match", 0)]:
        snapshot = frame(sequence)
        snapshot.update(selection_status=status, matching_stream_count=count, selected_stream=None)
        assert coordinator.accept(validate_snapshot(snapshot))
        assert entity.state == "idle"
        assert entity.extra_state_attributes == {
            "selection_status": status,
            "matching_stream_count": count,
        }
        for attribute in (
            "media_title",
            "media_artist",
            "media_album_name",
            "media_duration",
            "media_position",
            "media_position_updated_at",
            "media_content_id",
            "media_image_hash",
            "app_name",
        ):
            assert getattr(entity, attribute) is None
        assert not coordinator.missing_artwork()
    await hass.async_block_till_done()
    assert entry.data["source_filters_migrated"] is True
    coordinator.close()


@pytest.mark.parametrize(
    "status,count,selected",
    [
        ("selected", 0, True),
        ("selected", 1, False),
        ("no_match", 1, False),
        ("no_match", 0, True),
        ("ambiguous", 1, False),
        ("ambiguous", 2, True),
    ],
)
def test_status_null_count_consistency(status, count, selected):
    snapshot = frame()
    snapshot.update(selection_status=status, matching_stream_count=count)
    if not selected:
        snapshot["selected_stream"] = None
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


@pytest.mark.parametrize("key", ["playback_username", "player_name"])
@pytest.mark.parametrize("value", [False, 1, [], "x" * 4097, "é" * 2049, "nul\x00text"])
def test_source_filter_bounds(key, value):
    snapshot = frame()
    snapshot["filters"][key] = value
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


@pytest.mark.parametrize(
    "key",
    [
        "id",
        "title",
        "artist",
        "album",
        "coverArt",
        "username",
        "playerName",
        "playerId",
    ],
)
@pytest.mark.parametrize("value", ["é" * 2049, "nul\x00text"])
def test_selected_metadata_utf8_bounds(key, value):
    snapshot = frame()
    snapshot["selected_stream"][key] = value
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


@pytest.mark.parametrize("value", [None, "", 1, True])
def test_selected_requires_stable_string_player_id(value):
    snapshot = frame()
    snapshot["selected_stream"]["playerId"] = value
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


@pytest.mark.usefixtures("enable_custom_integrations")
async def test_http_mismatch_and_v1_upgrade_message(hass, hass_client_no_auth):
    entry, unused = receiver(hass, playback_username="saved")
    unused.close()
    assert await hass.config_entries.async_setup(entry.entry_id)
    await hass.async_block_till_done()
    client = await hass_client_no_auth()
    url = "/api/navidrome_nowplaying/stable-receiver"
    headers = {"Authorization": "Bearer private"}
    snapshot = frame()
    original = dict(entry.data)
    response = await client.post(url, headers=headers, json=snapshot)
    assert response.status == 409
    ack = await response.json()
    assert ack["accepted"] is False
    assert ack["accepted_sequence"] == 0
    assert ack["instance_id"] is None
    assert ack["missing_artwork"] == []
    assert "copy saved HA filters" in ack["error"]
    assert dict(entry.data) == original
    assert hass.states.get("media_player.navidrome").state == "unavailable"
    snapshot["version"] = 1
    response = await client.post(url, headers=headers, json=snapshot)
    assert response.status == 400
    assert "upgrade both components" in await response.text()
    snapshot["version"] = 2
    snapshot["filters"]["playback_username"] = "saved"
    response = await client.post(url, headers=headers, json=snapshot)
    assert (await response.json())["accepted"] is True
    await hass.async_block_till_done()
    assert entry.data["source_filters_migrated"] is True
    assert entry.data["playback_username"] == "saved"
    assert hass.states.get("media_player.navidrome").state == "paused"
    await hass.config_entries.async_unload(entry.entry_id)


@pytest.mark.parametrize("field", ["duration", "positionMs", "playbackRate"])
@pytest.mark.parametrize("value", [-1, True, 2**53 + 1, 10**400, float("nan"), float("inf")])
def test_all_numeric_metadata_bounds(field, value):
    snapshot = frame()
    snapshot["selected_stream"][field] = value
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


def test_never_accept_candidate_arrays():
    snapshot = frame()
    snapshot["streams"] = [snapshot["selected_stream"]]
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)


def test_required_fields_and_filter_pair():
    for key in frame():
        snapshot = frame()
        del snapshot[key]
        with pytest.raises((ValueError, TypeError)):
            validate_snapshot(snapshot)
    snapshot = frame()
    snapshot["filters"] = {"playback_username": None}
    with pytest.raises(ValueError):
        validate_snapshot(snapshot)
    snapshot = frame()
    snapshot["filters"]["playback_username"] = "é" * 2048
    assert validate_snapshot(snapshot)[-1][0] == "é" * 2048
