> **This repository is retired.** Wildskeeper lives in the
> [sampo monorepo](https://github.com/safwyls/sampo) now — `core/` (the
> shared console framework), `games/dragonwilds/`, `cmd/wildskeeper`,
> `cmd/wkagent`, `web/wildskeeper`, and `tools/` (dwbridge, the UE4SS
> kit, the wine shim). The port was verified against the live
> Dragonwilds server on 2026-08-16, and the `ghcr.io/safwyls/wildskeeper`
> / `ghcr.io/safwyls/wkagent` images (including `:latest` and
> `:latest-wine`) publish from sampo since then. History is preserved in
> sampo via subtree import. This repo is archived read-only; file issues
> and PRs against sampo.

# Wildskeeper (wildskeeper)

A management console for a self-hosted **RuneScape: Dragonwilds** dedicated
server — built on the reusable base extracted from
[palcon](https://github.com/safwyls/palcon), its sibling project for
Palworld.

Dragonwilds has no RCON, no HTTP admin API and no query protocol; all
native administration is the in-game Server Management menu. Wildskeeper
therefore *derives* everything: process liveness and uptime from its
wkagent sidecar, the live player list from a state machine over the
server's log tail, configuration from `DedicatedServer.ini` at rest.
Commands that have no transport (broadcast, kick, ban) answer HTTP 501
with the honest reason instead of pretending — they light up when the
planned UE4SS command bridge exists.

## What works today

- **Overview** — the rune sigil (six segments, one per player slot), power
  controls through the agent, uptime/player vitals, log preview
- **Adventurers** — who's online (log-derived), join/leave history and
  playtime via the collector
- **World saves** — snapshot, download, delete, scheduled backups of
  `Saved/SaveGames`
- **Configuration** — `DedicatedServer.ini` editor (never adds or removes
  keys, type-validated, one-level `.bak`, atomic swap) plus one-click
  admin-password rotation — the game's one real remote-admin lever
- **Server log** — live tail through the agent
- **Raise a server** — one-click provisioning through a wkagent
  provisioner (or a generated compose stack to deploy by hand): creates the
  container, installs the game via SteamCMD, seeds `DedicatedServer.ini`
  with your Owner ID, and starts it
- Shared base: users/roles/permissions, audit trail, Discord notifications,
  scheduled restarts, crash watchdog, SteamCMD update jobs (app id
  4019830), public status page

`docs/dragonwilds-recon.md` records every externally-verified game fact and
the open empirical gates (log line shapes, player-id format, save format,
SIGTERM behavior, on-disk ban list). Facts marked UNVERIFIED there are not
assumed anywhere in the code.

## Running it

```sh
cp .env.example .env && export $(cat .env | xargs)
go run ./cmd/wildskeeper          # backend on :8080
cd web && npm install && npm run dev   # frontend dev server
```

Production: `cd web && npm run build`, then `go build ./cmd/wildskeeper` (the Go
binary embeds the bundle), or use the `Dockerfile` / `docker-compose.yml`.

The game server itself runs under the `wkagent` sidecar
(`Dockerfile.wkagent`). In supervisor mode the agent *is* the server: it
installs the game with SteamCMD and runs
`./RSDragonwildsServer.sh -log -Port=7777` as a child process. It needs
`WKAGENT_OWNER_ID` — the game refuses to start without an owner, and the
agent seeds the config with it on first install. The Raise-a-server wizard
sets all of this up; `docs/sidecar-agent.md` has the reference stack.

## Testing against a real server

`scripts/dev-local.sh` stands the whole stack up locally — SteamCMD installs
the game, the agent supervises it, and the dashboard drives the agent. It is
the same shape as a real deployment on purpose: the game has no admin
transport, so anything that bypasses the agent isn't exercising the real
path.

```sh
./scripts/dev-local.sh install   # one-off, ~5 GB
./scripts/dev-local.sh up        # build + start agent and dashboard
./scripts/dev-local.sh start     # start the game
./scripts/dev-local.sh status    # health, info, players, metrics
./scripts/dev-local.sh down      # stop everything
```

Then open http://127.0.0.1:8080 (admin / localadmin123). To actually join
the server from the game, set `DWDEV_OWNER_ID` to your Player ID first
(in game: Settings, bottom-left "My Player ID") — otherwise a placeholder
is used, which boots fine but won't make you the Owner.

On Fedora, SteamCMD needs `sudo dnf install glibc.i686 libstdc++.i686` and a
`/etc/ssl/certs/ca-certificates.crt` symlink to
`/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem`; without them it reports
"needs to be online" and gives up. `install` prints the exact commands.

Tests: `go test ./...` and `cd web && npm test`.

## Lineage

The game-agnostic architecture — `game.Definition` registry, sidecar agent,
savecache, collector, backup/notify/sched/watchdog — is palcon's, kept
structurally identical so improvements can flow between the two projects.
`docs/porting-to-another-game.md` describes the seam. Palworld-specific
code was removed rather than ported; a second game would register beside
Dragonwilds the same way Dragonwilds registered beside Palworld.
