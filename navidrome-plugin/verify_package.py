#!/usr/bin/env python3
"""Check the packaged reactor's real exports and host imports (no WASM runtime needed)."""

import json
from pathlib import Path
import sys
import zipfile


class Reader:
    def __init__(self, data):
        self.data = data
        self.pos = 0

    def byte(self):
        result = self.data[self.pos]
        self.pos += 1
        return result

    def uint(self):
        result = 0
        for shift in range(0, 35, 7):
            value = self.byte()
            result |= (value & 127) << shift
            if value < 128:
                return result
        raise ValueError("invalid unsigned LEB128")

    def name(self):
        length = self.uint()
        end = self.pos + length
        if end > len(self.data):
            raise ValueError("truncated name")
        result = self.data[self.pos:end].decode("utf-8")
        self.pos = end
        return result

    def limits(self):
        flags = self.byte()
        self.uint()
        if flags & 1:
            self.uint()


def verify(path):
    with zipfile.ZipFile(path) as package:
        assert sorted(package.namelist()) == ["manifest.json", "plugin.wasm"], "unexpected ZIP entries"
        assert package.testzip() is None, "corrupt ZIP"
        manifest = json.loads(package.read("manifest.json"))
        assert isinstance(manifest.get("version"), str) and manifest["version"], "missing version"
        wasm = package.read("plugin.wasm")
    assert wasm[:8] == b"\0asm\1\0\0\0", "not WASM v1"
    reader = Reader(wasm)
    reader.pos = 8
    exports = set()
    imports = set()
    while reader.pos < len(wasm):
        section = reader.byte()
        length = reader.uint()
        end = reader.pos + length
        assert end <= len(wasm), "truncated WASM section"
        if section == 2:
            for _ in range(reader.uint()):
                module, name, kind = reader.name(), reader.name(), reader.byte()
                imports.add((module, name))
                if kind == 0:  # function type index
                    reader.uint()
                elif kind == 1:  # table
                    reader.byte()
                    reader.limits()
                elif kind == 2:  # memory
                    reader.limits()
                elif kind == 3:  # global
                    reader.byte()
                    reader.byte()
                else:
                    raise ValueError(f"unsupported import kind {kind}")
        elif section == 7:
            for _ in range(reader.uint()):
                name, kind = reader.name(), reader.byte()
                reader.uint()
                if kind == 0:
                    exports.add(name)
        reader.pos = end
    required = {
        "_initialize", "nd_on_init", "nd_scheduler_callback",
        "nd_scrobbler_is_authorized", "nd_scrobbler_now_playing",
        "nd_scrobbler_scrobble", "nd_scrobbler_playback_report",
    }
    assert required <= exports, f"missing function exports: {required - exports}"
    for name in ("http_send", "users_getusers", "subsonicapi_call", "subsonicapi_callraw",
                 "cache_getbytes", "cache_setbytes", "scheduler_scheduleonetime",
                 "scheduler_cancelschedule",
                 "kvstore_get", "kvstore_setwithttl", "kvstore_list", "kvstore_getmany"):
        assert ("extism:host/user", name) in imports, f"missing official host import: {name}"
    assert not any(name == "scheduler_schedulerecurring" for _, name in imports), "recurring scheduler imported"
    assert not any(name == "http_request" for _, name in imports), "Extism built-in HTTP imported"
    print(f"Validated {path}: manifest.json + plugin.wasm ({len(wasm):,} bytes)")
    print("Function exports: " + ", ".join(sorted(exports)))
    print("Official HTTP, users, SubsonicAPI, cache, KVStore and scheduler imports verified")


if __name__ == "__main__":
    verify(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "build/navidrome-plugin/navidrome-nowplaying.ndp")
