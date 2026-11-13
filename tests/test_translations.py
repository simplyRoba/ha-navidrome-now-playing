"""Keep bundled translations aligned with the canonical integration strings."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

INTEGRATION_DIR = Path(__file__).parents[1] / "custom_components" / "navidrome_nowplaying"


def _leaf_paths(value: Any, prefix: tuple[str, ...] = ()) -> set[tuple[str, ...]]:
    if not isinstance(value, dict):
        return {prefix}
    return {path for key, child in value.items() for path in _leaf_paths(child, (*prefix, key))}


def test_translation_keys_match_strings() -> None:
    canonical = json.loads((INTEGRATION_DIR / "strings.json").read_text())
    canonical_paths = _leaf_paths(canonical)

    for language in ("de", "en"):
        translated = json.loads((INTEGRATION_DIR / "translations" / f"{language}.json").read_text())
        assert _leaf_paths(translated) == canonical_paths
