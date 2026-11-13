"""UI-only configuration for Navidrome Now Playing."""

from __future__ import annotations

import secrets
from typing import Any
from uuid import uuid4

import voluptuous as vol
from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.helpers.network import NoURLAvailableError, get_url
from homeassistant.helpers.selector import BooleanSelector, TextSelector

from .const import CONF_PLAYBACK_USERNAME, CONF_PLAYER_NAME, DOMAIN
from .models import normalize_base_url


def _push_schema(defaults: dict[str, Any], *, reconfigure: bool = False) -> vol.Schema:
    fields = {
        vol.Required("ha_base_url", default=defaults.get("ha_base_url", "")): TextSelector(),
    }
    if reconfigure:
        fields[vol.Optional("rotate_secret", default=False)] = BooleanSelector()
    return vol.Schema(fields)


class NavidromeNowPlayingConfigFlow(ConfigFlow, domain=DOMAIN):
    """Pair push receivers and reconfigure existing entries."""

    VERSION = 1
    MINOR_VERSION = 3

    def _ha_url(self) -> str:
        try:
            return get_url(self.hass, allow_external=False, allow_cloud=False)
        except NoURLAvailableError:
            return ""

    def _push_data(
        self, user_input: dict[str, Any], existing: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        existing = existing or {}
        raw_url = user_input["ha_base_url"]
        if (
            not isinstance(raw_url, str)
            or len(raw_url) > 2048
            or any(c.isspace() for c in raw_url.strip())
        ):
            raise ValueError("invalid_url")
        base_url = normalize_base_url(raw_url)
        receiver_id = existing.get("receiver_id") or str(uuid4())
        secret = existing.get("secret")
        if not secret or user_input.get("rotate_secret") is True:
            secret = secrets.token_urlsafe(32)
        data = {
            "transport": "push",
            "ha_base_url": base_url,
            "receiver_id": receiver_id,
            "secret": secret,
            "push_url": f"{base_url}/api/navidrome_nowplaying/{receiver_id}",
        }
        for key in (CONF_PLAYBACK_USERNAME, CONF_PLAYER_NAME, "source_filters_migrated"):
            if key in existing:
                data[key] = existing[key]
        return data

    async def async_step_user(self, user_input=None) -> ConfigFlowResult:
        """New installations never collect account credentials."""
        errors = {}
        if user_input is not None:
            try:
                self._pending = self._push_data(user_input)
            except (ValueError, KeyError, TypeError):
                errors["base"] = "invalid_url"
            else:
                self._reconfiguring = False
                return await self.async_step_pairing()
        return self.async_show_form(
            step_id="user",
            data_schema=_push_schema(user_input or {"ha_base_url": self._ha_url()}),
            errors=errors,
        )

    async def async_step_pairing(self, user_input=None) -> ConfigFlowResult:
        """Show the dedicated secret; activate only when the user finishes."""
        if user_input is not None:
            if self._reconfiguring:
                entry = self._get_reconfigure_entry()
                # The first v2 ACK may migrate filters while this form is open.
                for key in (CONF_PLAYBACK_USERNAME, CONF_PLAYER_NAME, "source_filters_migrated"):
                    if key in entry.data:
                        self._pending[key] = entry.data[key]
                return self.async_update_reload_and_abort(
                    entry,
                    data=self._pending,
                    reason="reconfigure_successful",
                )
            return self.async_create_entry(title="Navidrome", data=self._pending)
        return self.async_show_form(
            step_id="pairing",
            data_schema=vol.Schema({}),
            description_placeholders={
                "push_url": self._pending["push_url"],
                "secret": self._pending["secret"],
            },
            last_step=True,
        )

    async def async_step_reconfigure(self, user_input=None) -> ConfigFlowResult:
        """Switch legacy entries to push, replacing rather than merging secrets."""
        entry = self._get_reconfigure_entry()
        # Legacy data contributes filters only; never read account credentials.
        existing = {
            key: entry.data[key]
            for key in (CONF_PLAYBACK_USERNAME, CONF_PLAYER_NAME, "source_filters_migrated")
            if key in entry.data
        }
        if entry.data.get("transport") == "push":
            for key in ("ha_base_url", "receiver_id", "secret", "push_url"):
                if key in entry.data:
                    existing[key] = entry.data[key]
        defaults = dict(existing)
        defaults.setdefault("ha_base_url", self._ha_url())
        errors = {}
        if user_input is not None:
            try:
                self._pending = self._push_data(user_input, existing)
            except (ValueError, KeyError, TypeError):
                errors["base"] = "invalid_url"
            else:
                self._reconfiguring = True
                return await self.async_step_pairing()
        return self.async_show_form(
            step_id="reconfigure",
            data_schema=_push_schema(defaults, reconfigure=bool(existing.get("secret"))),
            errors=errors,
        )
