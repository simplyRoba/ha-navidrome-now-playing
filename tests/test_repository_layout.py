"""HACS source-install layout and metadata checks."""

import json
import tomllib
from importlib.metadata import version
from pathlib import Path

from packaging.version import Version

ROOT = Path(__file__).resolve().parents[1]
INTEGRATION_DIR = ROOT / "custom_components" / "navidrome_nowplaying"


def test_project_metadata_uses_root_documents():
    project = tomllib.loads((ROOT / "pyproject.toml").read_text())["project"]
    assert project["readme"] == "README.md"
    assert project["license-files"] == ["LICENSE"]
    assert (ROOT / project["readme"]).is_file()
    assert (ROOT / "LICENSE").is_file()


def test_single_integration_at_repository_root():
    integrations = [
        path
        for path in (ROOT / "custom_components").iterdir()
        if path.is_dir() and not path.name.startswith(".") and path.name != "__pycache__"
    ]
    assert integrations == [INTEGRATION_DIR]
    assert (INTEGRATION_DIR / "__init__.py").is_file()


def test_hacs_metadata_uses_source_install():
    hacs = json.loads((ROOT / "hacs.json").read_text())
    manifest = json.loads((INTEGRATION_DIR / "manifest.json").read_text())

    assert hacs["name"] == manifest["name"]
    assert not hacs.get("content_in_root", False)
    assert not hacs.get("zip_release", False)
    assert Version(hacs["homeassistant"]) <= Version(version("homeassistant"))


def test_integration_has_hacs_manifest_and_brand_assets():
    manifest = json.loads((INTEGRATION_DIR / "manifest.json").read_text())

    assert {
        "domain",
        "name",
        "version",
        "documentation",
        "issue_tracker",
        "codeowners",
    } <= manifest.keys()
    assert manifest["domain"] == INTEGRATION_DIR.name
    assert manifest["documentation"].startswith("https://")
    assert manifest["issue_tracker"].startswith("https://")
    assert manifest["codeowners"]
    assert all(owner.startswith("@") for owner in manifest["codeowners"])
    assert Version(manifest["version"])
    assert (INTEGRATION_DIR / "brand" / "icon.png").read_bytes().startswith(b"\x89PNG\r\n\x1a\n")
