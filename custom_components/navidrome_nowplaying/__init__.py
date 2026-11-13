"""Navidrome Now Playing integration setup."""

from __future__ import annotations

from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant, callback
from homeassistant.exceptions import ConfigEntryError

from .const import PLATFORMS
from .push import RECEIVERS, PushCoordinator, register_receiver, register_views

type NavidromeConfigEntry = ConfigEntry[PushCoordinator]


async def async_setup(hass: HomeAssistant, config: dict) -> bool:
    """Register global receivers after the HTTP dependency is initialized."""
    register_views(hass)
    return True


async def async_setup_entry(hass: HomeAssistant, entry: NavidromeConfigEntry) -> bool:
    """Set up a push receiver without accessing any Navidrome account API."""
    if entry.data.get("transport") != "push":
        raise ConfigEntryError("Polling is no longer supported. Use Reconfigure to pair push.")
    if not all(entry.data.get(key) for key in ("receiver_id", "secret")):
        raise ConfigEntryError("Push pairing is incomplete. Use Reconfigure to pair push.")
    coordinator = PushCoordinator(hass, entry)
    entry.runtime_data = coordinator
    register_receiver(hass, entry.data["receiver_id"], coordinator)
    entry.async_on_unload(coordinator.close)

    @callback
    def unregister_receiver() -> None:
        hass.data[RECEIVERS].pop(entry.data["receiver_id"], None)

    entry.async_on_unload(unregister_receiver)
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    return True


async def async_unload_entry(hass: HomeAssistant, entry: NavidromeConfigEntry) -> bool:
    """Unload all platforms belonging to an entry."""
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
