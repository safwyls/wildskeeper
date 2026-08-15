# State of play

Written 2026-08-09 as a handoff; updated 2026-08-10 after the first real
deployment (see "Deployed for real", below). Read this first, then
[`dragonwilds-recon.md`](dragonwilds-recon.md) — between them they hold
every fact the code rests on and every place it is still guessing.

## What this is

**Wildskeeper** (module `github.com/safwyls/wildskeeper`) is a management console
for a self-hosted **RuneScape: Dragonwilds** dedicated server. It was built
by copying [palcon](https://github.com/safwyls/palcon) — its Palworld
sibling — and removing Palworld. The maintainer wanted a **separate repo**,
not a second game inside palcon; if you find advice to the contrary in
`dragonwilds-plan.md` §0, that decision was overridden and the plan says so
at the top.

The architecture is palcon's, kept structurally identical so fixes can
travel between the two. `porting-to-another-game.md` describes the seam.

## The one fact that shapes everything

Dragonwilds has **no RCON, no REST API, and no query protocol**. There is no
way to ask the game anything. Every piece of live state is *derived*:

| What the UI shows | Where it actually comes from |
|---|---|
| Server up/down, uptime | wkagent's `/v1/health` → supervised process state |
| Player list | a state machine (`dwlog`) over the agent's stdout log ring |
| Config | `DedicatedServer.ini` read at rest (`dwconfig`) |
| Saves | files on disk, synced through the agent |

So **the agent is not optional here** — it is the only transport. Anything
that bypasses it isn't testing the real path. Commands reach the game
through the **dwbridge** mod when it's running (Phase 4, below): `Save` is
live end to end, and the client routes each command through the bridge only
when the mod's heartbeat lists it. A command with nowhere to go — no bridge,
or a verb the mod hasn't implemented — returns `*game.UnsupportedError`,
which `internal/api/actions.go` maps to **HTTP 501**, deliberately distinct
from 502 so the UI can say "this game can't" rather than "the server is
unreachable". (`Shutdown` stays 501 by design: the agent's power controls
own stopping the process.)

## Where things stand

`go test ./...` and `cd web && npm test` green, production build fine.

**Done:** Phase 0 (recon), Phase 1 (game package + client + config + log
tracker), Phase 2 (agent launch profile + provisioning + Raise-a-server
wizard), Phase 3 (the `dwsave` save reader — world *metadata*, see below),
Phase 4 (the dwbridge command channel — its `save` verb works end to end;
see below), Phase 5 (the Wildskeeper frontend).

**Partial, and now bounded by the game rather than by effort:** Phase 4's
command surface. The bridge exists and `save` is proven through the whole
stack. `kick`/`ban`/`unban`/`broadcast` were implemented and tested against
a live connected player, found to silently do nothing, and **removed** — a
`Server_` RPC invoked on the server is a no-op, the native functions that
would work wedge the UE4SS Lua VM, and no Blueprint-exposed kick/ban exists
anywhere. Full detail in the recon doc's "Why the command tier stops at
`save`"; don't re-attempt the obvious path without reading it.
Also unfinished: everything in the save beyond the header —
`dwsave` reads the INFO chunk and level names, not players or inventories,
so the visibility roster still reports unavailable.

### Verified by hand against a real server

A live server was stood up and the whole stack driven through it. These
aren't assumptions:

- The agent starts/stops the game; config enforcement rewrites exactly
  `ServerName` / `AdminPassword` / `OwnerId` and leaves `ServerGuid`,
  `WorldPassword` and the `;METADATA` comment alone.
- `info` / `players` / `metrics` derive correctly (`transport: agent`,
  `maxplayernum: 6`, real uptime).
- Before the bridge, all six commands answered 501 with their real
  reasons; now `Save` runs through dwbridge (verified — see Phase 4) and the
  rest answer 501 until the mod implements them.
- Log tail, the ini editor, admin-password rotation, and a real backup of
  an actual SPUD save all work. Stop is clean and reports `stopped`, not
  `crashed`.
- `dwsave` (Phase 3) parses both the committed fixture and the live,
  five-autosaves-later world from the same install, and the GUID it
  renders is byte-for-byte the `WorldSaveGuid` the server writes in its
  own log — the decode is checked against the game, not just itself. Two
  clock facts worth knowing: the header's Z-suffixed timestamps actually
  record host-local time (trust the file's mtime instead), and
  `Meta_SaveFileRevision` counts up once per save — an autosave odometer.

## Things that will waste your time if you don't know them

1. **Saves are SPUD, not GVAS.** Magic `SAVE`, chunked, readable
   length-prefixed strings. `uesave` and gvas libraries will not open it.
   A real 57 KB fixture is at
   `internal/games/dragonwilds/testdata/world-empty.sav`.
2. **The game does not save on shutdown.** Clean stop is ~2 s, exit 143,
   world file byte-identical. Autosave is a CVar
   (`dom.StateSaveFrequencyMins:5`), not an ini key, and was observed
   firing exactly five minutes after a world-load save on an idle server.
   So a restart costs up to ~5 minutes of play — the UI says so, and it
   must keep saying so.
3. **`OwnerId` is required to boot but not validated.** The server refuses
   to start with it empty, yet boots happily on the literal string
   `test123`. So never reject an id for failing the shape.
4. **Player IDs are 32 hex chars, and the case varies by context** — the
   Settings screen shows lowercase, the server writes uppercase
   (`ServerGuid`, `WorldSaveGuid`). `CanonicalUID` folds case for
   `^[0-9a-fA-F]{32}$` and only trims anything else. Getting this wrong
   fails *open* on visibility checks.
5. **An idle server logs almost nothing** — an EOS session heartbeat every
   ~30 s and an autosave every 5 min, and that is all. Liveness must never
   be inferred from log activity.
6. **`RSDragonwildsServer.sh` is only a wrapper.** Killing it leaves the
   binary running; signals go to the process group.
7. **Steam and Epic both work.** The binary links both SDKs and EOS
   federates Steam logins, so "Epic auth" does not mean "needs an Epic
   account".

## A real client joined (2026-08-09), and most gaps closed

A player joined and left the local server. What that settled (details in
the recon doc's "Closed 2026-08-09" section):

1. **Join/leave lines: verified.** `dwlog` RulesV1 is written from the
   capture (committed corpus with synthetic ids), keys sessions by the
   real player id, and the Adventurers list now carries real ids through
   `CanonicalUID`.
2. **Bans: located.** The ini's `KnownPlayerList` holds id, name,
   privileges and a `bIsBanned` flag per known player. Whether the server
   honors a hand-edited flag is still untested.
3. **A leave writes state** — `PlayerStateSave result[true]` plus a world
   save at the same instant. The autosave *interval* under activity is
   still unmeasured.
4. **Player state in the save is JSON** (char record keyed by a character
   guid; the EOS id appears nowhere in the save, so identity always routes
   through log/ini). A played world save exists at
   `~/dwtest/server/.../World-75058.sav` for the deeper-parse work; it is
   deliberately not committed since it holds the maintainer's real ids.

Still open: the second UDP port question, ban *enforcement*, chat lines.

## Phase 4: the command channel exists, and `save` works end to end

The dwbridge mod (`tools/dwbridge`) is real, and one command is proven
through the whole stack: `POST /api/servers/{id}/save` in the console wrote
the world on a headless server with no player connected —
wildskeeper → wkagent `/v1/bridge/command` → file IPC → the UE4SS Lua mod →
`PersistenceSubsystem:SaveGame`, `Save completed SUCCESSFULLY` in the game
log, save file rewritten.

The pieces, all committed:
- **`tools/dwbridge`** — the Lua mod. Heartbeat + single-flight file IPC
  (`request.json`/`response.json`; fixed names because `io.popen` and
  rename-over-existing are unreliable under Wine). Commands: `ping`, `save`.
- **`tools/ue4ss-wine-shim`** — how the modded Windows build runs under Wine
  (the server imports no dwmapi, so UE4SS loads via a `version.dll` shim).
- **wkagent** — `bridge.go`: the file IPC's other half, `POST
  /v1/bridge/command`, and `health.bridge` (heartbeat freshness + command
  list). Supervisor mode only.
- **dragonwilds client** — commands route through the bridge when the
  heartbeat lists them; otherwise the honest 501 stands. `save` is live;
  the rest map to real functions but await the mod implementing them.
  It also answers `game.CommandProber` (2026-08-11): `Supports(ctx, op)`
  reports whether a command would be carried *without* carrying it, so the
  console can describe a server before anyone clicks. Probe and command
  share one decision (`bridgeReady`), which is what keeps a promise from
  drifting from the behaviour; `GET /servers/{id}/capabilities` exposes it,
  and a game whose client has no prober reports everything supported.
- **the dashboard** — on-demand save is a first-class control (2026-08-11):
  a Save world button in the Overview's power row (kept even for
  agent-managed servers, where docker power hides) and on the World-saves
  page, plus a "Save world, then stop/restart" action inside the power
  confirmations — the interactive half of roadmap item 13's
  warn → save → stop → start chain. Capability is still discovered by
  doing: no bridge means the button's toast relays the 501's reason.
- **the scheduler** — the *automatic* half of that chain, and the surprise
  when it was picked up (2026-08-11): `sched.restart` had called
  `client.Save` all along, so scheduled restarts have been reaching
  dwbridge since the mod landed. What was missing was knowing whether the
  save happened. It now classifies the attempt — saved / no command bridge
  (`*game.UnsupportedError`) / failed — and carries that into the Discord
  restart notice, the audit detail (`"05:00 · world saved"`, rendered in
  Activity) and the log. The notice used to be sent *before* the save and
  claim "is saving and restarting now" unconditionally; it is now sent
  after, and says which of the three happened. On a game that does not save
  on shutdown, that distinction is the difference between a clean restart
  and losing up to ~5 minutes, so it is worth the ≤25 s the save budget
  adds to the notice.

What's left in Phase 4, in the order worth attempting:

1. ~~**A wkagent launch profile for the Wine + Windows build.**~~ **Built
   2026-08-11, and untested against real Wine — see below.** The agent now
   has launch *profiles* (`internal/wkagent/launch.go`): `native` (the
   Linux build, no mods) and `wine` (the Windows build, the only one UE4SS
   can attach to). A profile carries everything that differs between the
   two builds, because it is more than a command line — different Steam
   depot (`+@sSteamCmdForcePlatformType windows`, which must precede
   `+login` or it is ignored), a different config directory
   (`WindowsServer/`, not `LinuxServer/` — the ini editor would otherwise
   edit a file the game never reads), a different "is it installed" probe,
   and the environment the mod stack needs:
   `WINEDLLOVERRIDES=version=n,b` (without it Wine prefers its builtin
   version.dll and UE4SS never injects) plus `DWBRIDGE_DIR` as a
   `Z:`-mapped Windows path (the mod reads it with Windows semantics; a
   Linux path leaves the bridge idle with no error anywhere).

   The selection persists in the install volume beside desired-state, is
   reported in `health.launch`, and is changed through `PUT /v1/launch` →
   `PUT /api/servers/{id}/launch` → a Launch mode control in the
   dashboard's power card. It deliberately applies at the *next start*:
   switching build is a re-install, not a restart, so the agent refuses to
   decide that timing. `deploy/`-wise there is a second image,
   `Dockerfile.wkagent-wine` — Wine adds >1 GB and most servers will never
   want it, so the plain image stays small.

   **Switching is safe for the world, and carries the settings.** Saves
   live in `Saved/SaveGames/`, which is *not* platform-suffixed, so both
   builds read and write the same world — switching costs nothing there.
   Config is a different story: it *is* per platform, so selecting a
   profile copies `DedicatedServer.ini` across to the new build's
   directory when that side has none, and the agent's config verbs follow
   the active profile. Without both, a switch would silently revert every
   edited setting and then let the dashboard keep editing a file the game
   no longer reads.

   **Deploying it.** The provisioner needs no change — it doesn't run
   games. The per-server agent container is what needs Wine, via the
   `latest-wine` image tag (Deployment details in the Raise-a-server
   wizard). CI publishes it from `Dockerfile.wkagent-wine`; the plain
   `latest` stays Wine-free.

   For a server that *already exists*, there is now a button:
   `POST /v1/provision/recreate` on the provisioner reads a container's
   configuration back with `dockerctl.InspectSpec` and rebuilds it on
   another image, keeping env, binds, ports, user, labels, restart policy
   and networks. This exists because provisioner-made containers belong to
   no orchestrator — they are not in a TrueNAS apps list or any compose
   file — so the alternative was hand-written docker on the host. The
   agent reports `launch.runnable` (is the launcher actually present in
   this image?), and the console offers the rebuild exactly when a Wine
   profile is selected on an image with no Wine in it. The image is pulled
   before anything is removed, so a bad tag fails while the old container
   is still running.

   **What is proven and what is not.** A stub `wine` on PATH proves the
   whole launch path — PATH resolution, the exe, the port, the env, the
   working directory — reaches the process
   (`TestWineProfileLaunchesThroughPathWithTheModEnvironment`). Nothing
   here has been run against real Wine, a real Windows depot, or a real
   UE4SS: this box has none of the three. The feasibility of the *stack*
   was proven by hand on 2026-08-09 (`tools/ue4ss-wine-shim/README.md`);
   what is unproven is this agent driving it. Expect the first real run to
   find something — a path that needs to be `Z:`-mapped and isn't, a Wine
   prefix permission, the exe name after a patch.
2. **Ban via the ini.** `KnownPlayerList`'s `bIsBanned` flag is the one
   plausible route to offline ban/unban left standing, and dwconfig already
   owns that file safely. Needs one experiment: does the server honour a
   hand-edited flag, and does it re-read the file?
3. **The `KickedUsers`/`BannedUsers` GameState arrays** — the only untried
   lead for a live kick, and a long shot (the disconnection itself is
   native code, so replicating the arrays may only move UI state).

Do **not** start by re-implementing the admin RPC: that path is closed and
the recon doc explains exactly why.

## Running it locally

```sh
./scripts/dev-local.sh install   # one-off, ~5 GB (already done on this box)
DWDEV_OWNER_ID="<32-hex id>" ./scripts/dev-local.sh up
./scripts/dev-local.sh start
./scripts/dev-local.sh status
./scripts/dev-local.sh down
```

Dashboard on `:8080` (`admin` / `localadmin123`). The install lives at
`~/dwtest/server`; the stack's runtime state at `~/dwtest/local`. **As of
this handoff all three processes are running.**

Two environment quirks, both already applied on this machine: SteamCMD is
32-bit and needs `glibc.i686` plus a `/etc/ssl/certs/ca-certificates.crt`
symlink to `/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem`, or it
reports "needs to be online" and gives up.

**Reaching the server from a Windows game client: solved.** WSL2's default
NAT mode forwards TCP only, and the game is UDP — but **`networkingMode=mirrored`
in `C:\Users\<user>\.wslconfig` (then `wsl --shutdown`) fixes it**, confirmed
2026-08-09: a client connected to the WSL-hosted server over UDP with no
trouble. Installing the Windows depot (`4019831`) natively is the fallback
if mirrored mode is ever unavailable.

## Suggested next steps

A fuller, prioritized version of this list now lives in
[`roadmap.md`](roadmap.md); the two items below remain the highest-value
engineering.

1. **Finish the dwbridge command set.** The channel and `save` are done;
   what remains is `kick`/`ban`/`unban`/`broadcast` in the mod. These need
   a connected client to build against (the admin RPC wants a player
   controller; the struct params want a real value to copy), and a
   wkagent launch profile that runs the Windows build under Wine so the
   agent supervises the modded process directly. Pin the UE4SS
   nightly that works; expect churn at 1.0.
2. **Deepen the save reader.** Now unblocked for real: a played world
   save exists locally, and player state turns out to be JSON embedded in
   SPUD properties — find the property values, `json.Unmarshal`, done.
   Keyed by char guid, not EOS id, so the roster still routes identity
   through dwlog/ini.
3. ~~Build both images before deploying to the NAS.~~ **Done, and the
   doubt was warranted.** Both images now build (first fix: FROM
   references needed registry qualification — podman-style engines
   enforce short-name resolution), and the whole stack ran containerized
   under rootless podman: agent healthy, **game boots and loads the world
   in-container**, metrics/world/logs derive across the container
   network, clean stop in ~1 s. The catch found on the very first run:
   **the game refuses to boot as root** ("Refusing to run with the root
   privileges", exit 134 crash loop), so `Dockerfile.wkagent` now bakes
   a `wkagent` user (uid 1000) and the generated compose warns that the
   `/dragonwilds` volume must be writable by that uid. The `-healthz`
   healthcheck is verified under docker-format builds (OCI builds drop
   HEALTHCHECK silently). ~~Still untouched by real infrastructure: the
   provisioner (fake API in tests only) and an actual SteamCMD install
   from inside the container.~~ **Both closed 2026-08-10** — see
   "Deployed for real" below.

## The Ilmari migration (2026-08-13, in progress)

Provisioning is moving out of wkagent into
[ilmari](https://github.com/safwyls/ilmari), one shared host service for
wildskeeper and palcon both (its `docs/migration.md` is the plan of
record). Phase 1 is done — Ilmari is deployed on the NAS read-only, and
every endpoint was confirmed against the real Docker socket, legacy-label
recognition included. The console side of Phase 2 is in:
`internal/ilmari` (the client), `api.Provisioner` (the interface both
implementations satisfy), and `api.IlmariProvisioner` — the adapter that
now owns the game knowledge the old provisioner held (WKAGENT_* env, the
UDP port pair, 8811, the image family, `/dragonwilds`). Setting
`ILMARI_URL`/`ILMARI_TOKEN` cuts the console over; unsetting them falls
back to `PROVISIONER_URL` untouched. One knowing regression: discovery
under Ilmari cannot read modes, so the legacy provisioner container shows
up as an adoption candidate until Phase 4 deletes it — adopt refuses it
with a clear message.

## Deployed for real (2026-08-10)

The whole production path now has one full success behind it, on a TrueNAS
SCALE box that also runs palcon:

- The repo lives at `github.com/safwyls/wildskeeper` and CI publishes
  `ghcr.io/safwyls/wildskeeper` + `ghcr.io/safwyls/wkagent`.
- The console and a provisioner-mode wkagent run as TrueNAS custom apps
  from `deploy/truenas-app.yaml` (compose yaml pasted into "Install via
  YAML"). Console runs as the TrueNAS `apps` user via `user: "568:568"`
  (the Dockerfile also takes `UID`/`GID` build args now).
- **One-click provisioning worked end to end with zero code changes**: the
  wizard → `/v1/provision` → container created → SteamCMD installed the
  game in-container on first boot → settings edited in the UI → restart →
  **a real game client connected and played.** The provisioner and
  in-container SteamCMD install are no longer test-fake-only.

Deployment lessons, so the next install doesn't rediscover them (all now
reflected in `deploy/truenas-app.yaml`'s comments):

1. **`ENCRYPTION_KEY` is exactly 32 bytes** — generate with
   `openssl rand -hex 16`, not `-hex 32` (that's 64 chars and the app
   exits at boot with a fatal log line). `JWT_SECRET` is ≥32, so
   `-hex 32` is right *there*.
2. **The dataset must be chowned to the runtime uid** (568:568 on
   TrueNAS) before first start, or `/data` fails with permission denied.
3. **Cross-app DNS** works by attaching both apps to the shared external
   `wildskeeper-net` — compose registers each *service name* as an alias
   on that network, so the console reaches `http://wkprovisioner:8811`.
   The `networks:` list is per-service; forgetting it on the provisioner
   is invisible until the console's health calls fail. TrueNAS may need a
   full redeploy (not restart) to pick up a network change.
4. **`WKAGENT_PUBLIC_HOST` wants the LAN IP**, not the WAN IP — it's the
   address the console itself uses to reach provisioned agents, and a WAN
   address means hairpin NAT.
5. **Port proposals can't see the other console's containers.** palcon's
   `palagent-palhalla` published host 8811 and the first provision failed
   at `docker start` with "port is already allocated" — created but not
   started, so the leftover container had to be `docker rm`'d before
   retrying (the 409 name gate is checked before creation, not after a
   half-failure). Fix was choosing agent port 8821 under the wizard's
   Deployment details. When two control planes share a host, eyeball
   `docker ps` for the proposed ports first.

## Loose ends

- **~1,100 lines of orphaned advisor backend** (`internal/advisor`,
  `internal/api/advisor.go`) — a Palworld pal advisor whose UI was deleted
  and whose `/pals` data source no longer exists. It compiles and is
  unreachable. Removing it was offered and not yet answered.
- `internal/rcon` and `rcontest` ship with no importer outside their own
  tests — kept as inherited generic packages, currently inert.
- Shared-layer tests use a test-only REST game in `internal/game/gametest`,
  because Dragonwilds itself is a poor instrument for testing the shared
  plumbing. **Production code must never import it.**
- The maintainer's real Player ID is deliberately **not** in this repo;
  every committed example is synthetic.
