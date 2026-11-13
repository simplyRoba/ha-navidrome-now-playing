# Contributing

## Home Assistant

Use Python 3.14 with HA 2026.10.0, or Python 3.13 with HA 2026.2.3.

From the repository root, for Python 3.14:

```sh
python3.14 -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[dev]' 'homeassistant==2026.10.0'
ruff format --check .
ruff check .
pytest --cov=custom_components.navidrome_nowplaying --cov-report=term-missing --cov-fail-under=80 -q
```

For Python 3.13, substitute `python3.13` and `homeassistant==2026.2.3`. Minimum coverage: 80%.

## Navidrome

Requires Go 1.25+, `make` and Python 3. Use `GO=/absolute/path/to/go` for a custom executable.

From the repository root:

```sh
make -C navidrome-plugin test
make -C navidrome-plugin
make -C navidrome-plugin verify
sh -n navidrome-plugin/build.sh
```

From `navidrome-plugin/`:

```sh
go test -race ./...
test -z "$(gofmt -l .)"
```

Docker package check, from the same directory:

```sh
./build.sh
python3 verify_package.py ../build/navidrome-plugin/navidrome-nowplaying.ndp
```

Unit tests need no Docker daemon; package and live deployment checks do. Shared fixtures: [`protocol/fixtures/`](protocol/fixtures/).

## Change constraints

- Preserve entry/entity IDs and existing push pairing.
- Keep requests, bodies, retries and caches bounded.
- Never log secrets or add account credentials/polling clients.
- Test receiver/media proxy, ordering/recovery, filters, artwork and the injected-host engine.
- Keep HA manifest, HA `pyproject.toml` and plugin manifest versions aligned for releases.
