"""Push config-entry setup, upgrade and reload lifecycle tests."""

from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from homeassistant.exceptions import ConfigEntryError
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.navidrome_nowplaying import async_setup_entry
from custom_components.navidrome_nowplaying.const import DOMAIN
from custom_components.navidrome_nowplaying.push import RECEIVERS


@pytest.mark.parametrize("data", [{}, {"password": "untouched"}, {"transport": "push"}])
async def test_unpaired_entries_fail_with_reconfigure_message(data):
    entry = MagicMock(data=data)
    with pytest.raises(ConfigEntryError, match="Reconfigure.*push"):
        await async_setup_entry(MagicMock(), entry)


@pytest.mark.usefixtures("enable_custom_integrations")
@pytest.mark.parametrize("minor_version", [1, 3])
async def test_push_upgrade_reload_preserves_pairing(hass, minor_version):
    data = {
        "transport": "push",
        "receiver_id": "receiver",
        "secret": "dedicated-secret",
        "ha_base_url": "http://homeassistant:8123",
        "push_url": "http://homeassistant:8123/api/navidrome_nowplaying/receiver",
        "playback_username": "listener",
        "player_name": "Kitchen",
    }
    entry = MockConfigEntry(domain=DOMAIN, data=data, version=1, minor_version=minor_version)
    entry.add_to_hass(hass)
    with patch("aiohttp.ClientSession.get", side_effect=AssertionError("No outbound API")):
        assert await hass.config_entries.async_setup(entry.entry_id)
        await hass.async_block_till_done()
        assert dict(entry.data) == data
        assert hass.states.get("media_player.navidrome").state == "unavailable"
        first = entry.runtime_data
        from homeassistant.util import dt as dt_util

        from custom_components.navidrome_nowplaying.push import validate_snapshot

        snapshot = validate_snapshot(
            {
                "version": 2,
                "instance_id": "running",
                "sequence": 42,
                "snapshot_at": dt_util.utcnow().isoformat(),
                "selected_stream": None,
                "selection_status": "no_match",
                "matching_stream_count": 0,
                "filters": {"playback_username": "listener", "player_name": "Kitchen"},
            }
        )
        assert first.accept(snapshot)
        await hass.async_block_till_done()
        assert hass.states.get("media_player.navidrome").state == "idle"
        assert await hass.config_entries.async_reload(entry.entry_id)
        await hass.async_block_till_done()
        assert first is not entry.runtime_data
        assert first._cancel_timeout is None
        assert dict(entry.data) == dict(data, source_filters_migrated=True)
        assert hass.data[RECEIVERS]["receiver"] is entry.runtime_data
        assert hass.states.get("media_player.navidrome").state == "unavailable"
        assert entry.runtime_data.accept(snapshot)
        await hass.async_block_till_done()
        assert hass.states.get("media_player.navidrome").state == "idle"
        assert await hass.config_entries.async_unload(entry.entry_id)
        assert "receiver" not in hass.data[RECEIVERS]


async def test_forwarding_receives_runtime_data(hass):
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={
            "transport": "push",
            "receiver_id": "receiver",
            "secret": "secret",
        },
    )
    entry.add_to_hass(hass)
    hass.data[RECEIVERS] = {}
    with patch.object(
        hass.config_entries, "async_forward_entry_setups", new=AsyncMock()
    ) as forward:
        assert await async_setup_entry(hass, entry)
        assert hass.data[RECEIVERS]["receiver"] is entry.runtime_data
        forward.assert_awaited_once()
    entry.runtime_data.close()
