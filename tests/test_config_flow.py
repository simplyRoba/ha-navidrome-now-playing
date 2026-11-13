"""Practical push-only config-flow validation tests."""

from __future__ import annotations

from unittest.mock import AsyncMock, patch

import pytest
from homeassistant.config_entries import SOURCE_RECONFIGURE, SOURCE_USER
from homeassistant.data_entry_flow import FlowResultType
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.navidrome_nowplaying.const import (
    CONF_PLAYBACK_USERNAME,
    CONF_PLAYER_NAME,
    DOMAIN,
)


@pytest.mark.usefixtures("enable_custom_integrations")
async def test_new_push_setup_and_pairing(hass):
    initial = await hass.config_entries.flow.async_init(DOMAIN, context={"source": SOURCE_USER})
    assert initial["step_id"] == "user"
    assert set(str(key) for key in initial["data_schema"].schema) == {"ha_base_url"}
    result = await hass.config_entries.flow.async_configure(
        initial["flow_id"],
        {
            "ha_base_url": "https://ha.example/prefix/",
        },
    )
    assert result["step_id"] == "pairing"
    assert (
        "https://ha.example/prefix/api/navidrome_nowplaying/"
        in result["description_placeholders"]["push_url"]
    )
    assert len(result["description_placeholders"]["secret"]) >= 43
    with patch(
        "custom_components.navidrome_nowplaying.async_setup_entry", new=AsyncMock(return_value=True)
    ):
        result = await hass.config_entries.flow.async_configure(result["flow_id"], {})
    assert result["type"] is FlowResultType.CREATE_ENTRY
    assert result["data"]["transport"] == "push"
    assert CONF_PLAYBACK_USERNAME not in result["data"]
    assert CONF_PLAYER_NAME not in result["data"]
    assert "password" not in result["data"]
    assert "username" not in result["data"]


@pytest.mark.usefixtures("enable_custom_integrations")
@pytest.mark.parametrize(
    "url",
    [
        "ftp://ha.example",
        "https://u:p@ha.example",
        "https://ha.example?x=1",
        "https://ha.example#x",
        "http://ha.example:0",
        "http://ha .example",
        "http://" + "a" * 2048,
        None,
    ],
)
async def test_invalid_ha_url(hass, url):
    result = await hass.config_entries.flow.async_init(
        DOMAIN, context={"source": SOURCE_USER}, data={"ha_base_url": url}
    )
    assert result["errors"] == {"base": "invalid_url"}


@pytest.mark.usefixtures("enable_custom_integrations")
async def test_migrate_and_rotate_push(hass):
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={
            "base_url": "https://music.example",
            "username": "api-user",
            "password": "secret",
            "verify_ssl": True,
            "poll_interval": 5,
            CONF_PLAYBACK_USERNAME: "listener",
            CONF_PLAYER_NAME: "Kitchen",
        },
    )
    entry.add_to_hass(hass)
    initial = await hass.config_entries.flow.async_init(
        DOMAIN, context={"source": SOURCE_RECONFIGURE, "entry_id": entry.entry_id}
    )
    assert "rotate_secret" not in initial["data_schema"].schema
    assert set(str(key) for key in initial["data_schema"].schema) == {"ha_base_url"}
    result = await hass.config_entries.flow.async_configure(
        initial["flow_id"], {"ha_base_url": "http://ha.example"}
    )
    assert result["step_id"] == "pairing"
    assert "password" in entry.data  # Not activated before Finish.
    with patch.object(hass.config_entries, "async_schedule_reload") as reload:
        result = await hass.config_entries.flow.async_configure(result["flow_id"], {})
    assert result["reason"] == "reconfigure_successful"
    reload.assert_called_once_with(entry.entry_id)
    assert set(entry.data) == {
        "transport",
        "ha_base_url",
        "push_url",
        "receiver_id",
        "secret",
        CONF_PLAYBACK_USERNAME,
        CONF_PLAYER_NAME,
    }
    original_secret = entry.data["secret"]
    original_receiver = entry.data["receiver_id"]
    for rotate in (False, True):
        initial = await hass.config_entries.flow.async_init(
            DOMAIN, context={"source": SOURCE_RECONFIGURE, "entry_id": entry.entry_id}
        )
        assert "rotate_secret" in initial["data_schema"].schema
        result = await hass.config_entries.flow.async_configure(
            initial["flow_id"],
            {
                "ha_base_url": "https://new.example/ha/",
                "rotate_secret": rotate,
            },
        )
        with patch.object(hass.config_entries, "async_schedule_reload"):
            await hass.config_entries.flow.async_configure(result["flow_id"], {})
        assert entry.data["receiver_id"] == original_receiver
        assert (entry.data["secret"] != original_secret) is rotate
        assert entry.data["push_url"].startswith("https://new.example/ha/api/")
        assert entry.data[CONF_PLAYER_NAME] == "Kitchen"
        assert entry.data[CONF_PLAYBACK_USERNAME] == "listener"


@pytest.mark.usefixtures("enable_custom_integrations")
async def test_rotation_form_preserves_migration_completed_while_open(hass):
    entry = MockConfigEntry(
        domain=DOMAIN,
        data={
            "transport": "push",
            "receiver_id": "receiver",
            "secret": "original",
            "ha_base_url": "http://ha.example",
            "playback_username": " saved ",
            "player_name": None,
        },
    )
    entry.add_to_hass(hass)
    initial = await hass.config_entries.flow.async_init(
        DOMAIN, context={"source": SOURCE_RECONFIGURE, "entry_id": entry.entry_id}
    )
    assert set(str(key) for key in initial["data_schema"].schema) == {
        "ha_base_url",
        "rotate_secret",
    }
    rotate_key = next(key for key in initial["data_schema"].schema if str(key) == "rotate_secret")
    assert rotate_key.default() is False
    result = await hass.config_entries.flow.async_configure(
        initial["flow_id"], {"ha_base_url": "http://ha.example", "rotate_secret": True}
    )
    hass.config_entries.async_update_entry(
        entry, data=dict(entry.data, source_filters_migrated=True)
    )
    with patch.object(hass.config_entries, "async_schedule_reload"):
        await hass.config_entries.flow.async_configure(result["flow_id"], {})
    assert entry.data["source_filters_migrated"] is True
    assert entry.data["playback_username"] == " saved "
    assert entry.data["player_name"] is None
    assert entry.data["receiver_id"] == "receiver"
    assert entry.data["secret"] != "original"
