"""Constants for Navidrome Now Playing."""

from homeassistant.const import Platform

DOMAIN = "navidrome_nowplaying"
PLATFORMS: list[Platform] = [Platform.MEDIA_PLAYER]

CONF_PLAYBACK_USERNAME = "playback_username"
CONF_PLAYER_NAME = "player_name"

MAX_JSON_RESPONSE_BYTES = 1024 * 1024
MAX_ARTWORK_BYTES = 10 * 1024 * 1024
PUSH_TIMEOUT_SECONDS = 300
