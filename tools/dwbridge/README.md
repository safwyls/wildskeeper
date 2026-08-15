# dwbridge — the Dragonwilds command channel

RuneScape: Dragonwilds ships no RCON, REST, or query protocol, so Wildskeeper
derives everything it shows from logs and files. The one thing it cannot
derive is *action* — save-now, kick, ban, broadcast. dwbridge is that missing
channel: a [UE4SS](https://github.com/UE4SS-RE/RE-UE4SS) Lua mod that calls the
game's own functions on request.

Proven end to end 2026-08-09: `POST /api/servers/{id}/save` in the console
wrote the world on a headless server with no player connected —
wildskeeper → wkagent → this mod → `PersistenceSubsystem:SaveGame`.

## How it fits together

```
wildskeeper  ──HTTP──▶  wkagent  ──files──▶  dwbridge (this mod)  ──▶  game
       /save      /v1/bridge/command    DWBRIDGE_DIR               UFunction
```

The agent and the mod share a directory (`<install>/dwbridge/`, the mod sees
it as `DWBRIDGE_DIR`). The transport is files because that is the one thing
they reliably share across the Wine boundary — no sockets, no ports. It is
single-flight (the agent serializes commands), so the files have fixed names:

| file            | writer | meaning                                             |
|-----------------|--------|-----------------------------------------------------|
| `heartbeat.json`| mod    | every ~2 s: liveness + the commands this build has  |
| `state.json`    | mod    | every ~2 s: live telemetry — player roster with pawn positions, and the in-game clock once its property names are confirmed. Same freshness rule as the heartbeat; read via the agent's `GET /v1/bridge/state`. |
| `request.json`  | agent  | `{"id","command","args"}`                            |
| `response.json` | mod    | `{"id","ok","error","data"}`, echoing the id         |

The agent reports the heartbeat (fresh within 8 s) as `health.bridge`, and
only routes commands the heartbeat lists — so shipping a new verb is adding a
handler here, never a version handshake.

## Commands

- `ping` — liveness with a real object touch (finds the live GameMode).
- `save` — `PersistenceSubsystem:SaveGame`, the same call the autosave makes.
  **Verified headless and through the console.**

That is the whole list, and the list is the contract: the agent only routes
what the heartbeat advertises, so the console reports everything else as an
honest capability gap.

### Why kick/ban/broadcast are not here

They were implemented, tested against a live connected player, and removed —
the full findings are in the recon doc's "Why the command tier stops at
`save`". The short version:

- **`Server_` RPCs do nothing when called on the server.** The reflected
  function is the client's send-stub; the logic is native `_Implementation`
  code that reflection can't reach. `Server_RequestAdminAction` returns
  `ok=true` and the player stays connected.
- **Native UFunctions wedge the UE4SS Lua VM.** `ClientMessage` and
  `ClientWasKicked` each hung the mod thread (the game itself keeps running);
  recovery needs a server restart. That rules out UE's standard kick path.
- **No Blueprint-exposed kick/ban exists** on the GameSession, GameMode or
  GameState.

A verb that answers "done" while nothing happened is worse than no verb —
an operator would be told a griefer was kicked while they were still in the
world. So the mod advertises only what it can actually do.

If you pick this up: the untried lead is the GameState's replicated
`KickedUsers` / `BannedUsers` arrays (readable today, empty on a fresh
server), and for bans the ini's `KnownPlayerList` `bIsBanned` flag.

## Install

1. Stand up the Windows server build under Wine with UE4SS — see
   `../ue4ss-wine-shim/` (the server imports no dwmapi, so UE4SS loads via a
   `version.dll` shim).
2. Copy this folder to
   `<server>/RSDragonwilds/Binaries/Win64/ue4ss/Mods/dwbridge/` and add
   `dwbridge : 1` to `ue4ss/Mods/mods.txt`.
3. Launch with `DWBRIDGE_DIR` set to the shared directory. Under Wine that is
   the `Z:`-mapped install path, e.g.
   `DWBRIDGE_DIR='Z:\...\dwbridge'`, pointing at the same `<install>/dwbridge`
   the agent uses.

## Gotchas worth knowing before you edit this

- **Wine rename semantics.** Windows `rename` does not overwrite an existing
  file (it fails and strands the `.tmp`), so the mod removes the destination
  first. Getting this wrong freezes `heartbeat.json` at its first value and
  the bridge reads as permanently stale — see `writeFileAtomic`.
- **Never call native UFunctions.** They hang the Lua VM (see above). Stick
  to Blueprint-exposed functions and property access.
- **Never nest `ForEachProperty` inside `ForEachFunction`.** That
  combination also wedged the VM during exploration.
- **A wedged mod is invisible from in-game.** The server keeps running and
  players stay connected; only the heartbeat stops. That is by design on the
  agent side — staleness is what marks the bridge unavailable — but it means
  "the console lost the bridge" and "the server is fine" are both true at
  once, and recovery is a server restart.
