"""Release version guards and binary-only publishing."""

import json
import os
import shlex
import subprocess
import sys
from pathlib import Path

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def workflow():
    return yaml.load(
        (ROOT / ".github/workflows/publish-release.yml").read_text(), Loader=yaml.BaseLoader
    )


@pytest.fixture
def release_source(tmp_path):
    (tmp_path / "pyproject.toml").write_text('[project]\nversion = "0.4.1"\n')
    for name, value in {
        "custom_components/navidrome_nowplaying/manifest.json": {"version": "0.4.1"},
        "navidrome-plugin/manifest.json": {"version": "0.4.1"},
        ".release-please-manifest.json": {".": "0.4.1"},
    }.items():
        path = tmp_path / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value))
    return tmp_path


def run_version_check(workflow, source, tag):
    step = next(
        step
        for step in workflow["jobs"]["build"]["steps"]
        if step["name"] == "Check release versions"
    )
    env = dict(os.environ, RELEASE_TAG=tag)
    env["PATH"] = str(Path(sys.executable).parent) + os.pathsep + env["PATH"]
    return subprocess.run(
        ["bash", "-e", "-o", "pipefail", "-c", step["run"]],
        cwd=source,
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )


@pytest.mark.parametrize("tag", ["v0.4.1", ""])
def test_matching_versions_accept_release_or_dry_run(workflow, release_source, tag):
    result = run_version_check(workflow, release_source, tag)
    assert result.returncode == 0, result.stderr


@pytest.mark.parametrize("tag", ["v0.4.2", "0.4.1"])
def test_wrong_release_tag_is_rejected(workflow, release_source, tag):
    result = run_version_check(workflow, release_source, tag)
    assert result.returncode != 0
    assert "does not match component version" in result.stderr


@pytest.mark.parametrize(
    "name,key",
    [
        ("custom_components/navidrome_nowplaying/manifest.json", "version"),
        ("navidrome-plugin/manifest.json", "version"),
        (".release-please-manifest.json", "."),
    ],
)
def test_component_version_mismatch_is_rejected(workflow, release_source, name, key):
    (release_source / name).write_text(json.dumps({key: "0.4.2"}))
    result = run_version_check(workflow, release_source, "v0.4.1")
    assert result.returncode != 0
    assert "Component versions do not match" in result.stderr


@pytest.mark.parametrize("value", [None, "", 123])
def test_invalid_version_is_rejected(workflow, release_source, value):
    (release_source / "navidrome-plugin/manifest.json").write_text(json.dumps({"version": value}))
    result = run_version_check(workflow, release_source, "v0.4.1")
    assert result.returncode != 0
    assert "Release versions must be nonempty strings" in result.stderr


def test_manual_runs_cannot_publish(workflow):
    assert workflow["on"]["release"]["types"] == ["published"]
    assert workflow["on"]["workflow_dispatch"]["inputs"]["ref"]["default"] == "main"
    assert workflow["permissions"] == {"contents": "read"}
    publish = workflow["jobs"]["publish"]
    assert publish["if"] == "github.event_name == 'release'"
    assert publish["needs"] == "build"
    assert publish["permissions"] == {"contents": "write"}


def test_build_uses_release_tag_and_runs_verification(workflow):
    steps = workflow["jobs"]["build"]["steps"]
    checkout = next(step for step in steps if step["name"] == "Check out release source")
    assert checkout["with"]["ref"] == (
        "${{ github.event_name == 'release' && "
        "format('refs/tags/{0}', github.event.release.tag_name) || inputs.ref }}"
    )
    assert checkout["with"]["persist-credentials"] == "false"
    commands = {step["name"]: step for step in steps if "run" in step}
    assert commands["Test and vet"]["run"] == "make -C navidrome-plugin test"
    assert commands["Check race safety"]["working-directory"] == "navidrome-plugin"
    assert commands["Check race safety"]["run"] == "go test -race ./..."
    assert commands["Build and verify package"]["run"] == "make -C navidrome-plugin all verify"


def test_only_verified_plugin_is_passed_between_jobs(workflow):
    build_steps = workflow["jobs"]["build"]["steps"]
    upload = next(step for step in build_steps if step["name"] == "Upload verified build")
    assert upload["with"]["path"] == "build/navidrome-plugin/navidrome-nowplaying.ndp"
    assert upload["with"]["if-no-files-found"] == "error"
    publish_steps = workflow["jobs"]["publish"]["steps"]
    download = next(step for step in publish_steps if step["name"] == "Download verified build")
    assert download["with"]["name"] == upload["with"]["name"]
    assert download["with"]["path"] == "release-artifacts"


def test_release_upload_contains_only_the_plugin(workflow):
    publish = workflow["jobs"]["publish"]
    upload = next(step for step in publish["steps"] if "run" in step)
    assert shlex.split(upload["run"]) == [
        "gh",
        "release",
        "upload",
        "$RELEASE_TAG",
        "release-artifacts/navidrome-nowplaying.ndp",
        "--repo",
        "$GITHUB_REPOSITORY",
    ]
