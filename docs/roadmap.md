# Roadmap

Written 2026-08-10, the day the stack first ran for real (TrueNAS, one-click
provision, live client). Ordered by value-per-effort within each horizon.
Ground rules inherited from the architecture: the agent is the only
transport, the console never gains docker create rights, and anything the
game can't do gets an honest 501 — features below must not bend those.

## Now — finish what's started

1. **Wine launch profile for the modded build** (state-of-play, Phase 4
   item 1). Pure engineering, no unknowns: a wkagent profile that runs the
   Windows build + UE4SS under Wine so the agent supervises the modded
   process instead of it being hand-run. This is the gate in front of every
   other dwbridge improvement — without it the command channel only exists
   on dev boxes. Pin the UE4SS nightly; expect churn at Dragonwilds 1.0.
2. **Ban via the ini** (Phase 4 item 2). One experiment stands between us
   and offline ban/unban: hand-flip `bIsBanned` in `KnownPlayerList`, see
   if the server honors it and when it re-reads the file. dwconfig already
   owns that file safely; if the flag works, `ban`/`unban` become config
   operations and skip the closed RPC path entirely.
3. **Deepen `dwsave`: the player roster.** Player state in the save is
   JSON inside SPUD properties, and a played world save exists locally.
   Find the property values, `json.Unmarshal`, surface characters (name,
   level, position, last-seen) on the Saves/world page. Identity still
   routes through dwlog/ini (the EOS id isn't in the save).
4. **Decide the orphaned advisor** (~1,100 dead lines, `internal/advisor`
   + `internal/api/advisor.go`). Either delete it or re-point it at
   Dragonwilds as a "world advisor" over dwsave data. Deletion is the
   cheap, reversible default; it's all in git history.

## Next — deployment quality of life

Everything here was motivated by a real friction on 2026-08-10:

5. **Startup config validation that names every problem at once.** The
   first deploy died one env var at a time. Validate all of
   JWT_SECRET/ENCRYPTION_KEY/ADMIN_PASSWORD/DATA_DIR in one pass and
   report the full list in one fatal message.
6. **Host-port awareness beyond our own containers.** The first provision
   failed on a port palcon held. The provisioner already talks to docker:
   have `/v1/provision` (or the defaults endpoint) list *all* containers'
   published host ports — not just wkagent-shaped ones — so proposals
   avoid them and a collision is refused before create, not discovered at
   start. Keeps the created-but-not-started leftover from ever existing
   (and if start still fails, clean up the half-made container).
7. **Provisioner status surfaced in the UI.** "No provisioner is
   configured" covered three different failures (unset URL, DNS,
   token). A settings/status page that shows the configured endpoint,
   last health result, and last error would have cut the debugging to
   one glance. Same panel can host a "test connection" button.
8. **A cleaner failed-provision story.** Today: 502 with the docker error
   inlined and a manual `docker rm`. Wanted: the error classified
   (port, name, pull, permission), the remedy stated, and the leftover
   container removed or adoptable.
9. **TLS / cross-host agents.** The pinned-fingerprint TLS scheme from
   sidecar-agent.md, so agents on other machines aren't plain HTTP with a
   bearer token. The deferred reverse-connection (agent dials out over
   WebSocket) unlocks NAT'd hosts; verb surface unchanged.

## Later — features on top of a solid base

10. **World map / viewer.** Once dwsave yields positions, a map page
    (player last-positions, points of interest) is the showpiece feature.
    Needs coordinate-system recon against the real game first.
11. **Play-session analytics.** dwlog already keys sessions by player id;
    persist them into per-player playtime, first/last seen, session
    history, and a small activity chart on the server page.
12. **Backup restore.** Backups exist; restore is still "the operator
    untars by hand". A deliberate restore verb on the agent (the one thing
    the read-only save mount was waiting for), gated in the UI behind a
    stopped server and a confirmation naming the save it overwrites.
13. ~~**Scheduled-restart polish for the no-save-on-shutdown game.**~~
    **Done (2026-08-11), and it was smaller than it looked.** The
    scheduler already called `client.Save` before restarting, so the
    warn → save → stop → start chain has been reaching dwbridge since the
    mod landed — what was missing was any way to know whether the save
    happened. The three outcomes (saved / no command bridge / failed) are
    now distinguished by `errors.As` on `*game.UnsupportedError`, and each
    one reaches the Discord restart notice, the audit trail (visible in
    Activity) and the log. Nothing claims a save it didn't make.
    The forward-looking half is done too: `game.CommandProber`
    (`Supports(ctx, op)`) is served by the dragonwilds client from the
    agent's `health.bridge` command list and exposed at
    `GET /servers/{id}/capabilities`, so the Automation page, the power
    row and the World-saves page describe *this* server rather than
    listing both possibilities. The probe and the command share one
    decision (`bridgeReady`) so they cannot drift, and a game with no
    prober reports everything supported — the optimism every caller had
    before it existed.
14. **Update automation.** SteamCMD update through the agent exists as a
    verb; add update checking (build id polling), a "update available"
    badge, and an opt-in maintenance window that chains save → stop →
    update → start.
15. **Chat capture, if it exists in the log.** Still-open recon question.
    If chat lines appear in the server log, dwlog grows a rule and the UI
    gets a live chat panel (read-only; there is no send path).
16. **Mod management via the agent.** The Wine/UE4SS stack (item 1) makes
    mod files part of the deployment; a small manifest the agent applies
    (dwbridge version pinning included) turns "the mod setup" from a doc
    into a button.

## Structural, someday

17. **Extract the shared base into a library palcon and wildskeeper both
    import.** The repos are kept structurally identical so fixes travel by
    hand; a `go.mod`-level shared core would make them travel by `go get`.
    Big, and only worth it if a third game ever appears — the current
    copy-discipline works and keeps each repo independently deployable.
    (A cheaper interim: a script that diffs the shared layers between the
    two repos and reports drift.)
18. **Multi-node.** Several game hosts, one console: provisioner-per-host
    is already the model; what's missing is TLS (item 9), per-host
    provisioner rows instead of one env var, and host labels in the UI.
19. **Auth beyond bootstrap admin.** Users/roles exist; OIDC/proxy-auth
    would let a TrueNAS/Authentik household SSO into it. Cookie-secure
    and JWT plumbing are already in place.

## Non-goals, so they don't creep back

- **Kick/broadcast via the admin RPC.** Tested against a live player,
  silently does nothing, and the working-looking alternatives wedge the
  Lua VM. The recon doc's "Why the command tier stops at `save`" is the
  tombstone; don't re-attempt without new information (a game update
  changing the surface *is* new information).
- **A query/RCON client.** The game has none; everything is derived, and
  the derivations are now verified. Don't add speculative protocol code.
- **The console holding docker create rights.** The provisioner split is
  the security model, not an inconvenience.

## Extracting the host provisioner (decided 2026-08-11)

Measured against palcon at `82aee47`, with naming normalised away, the two
agents' `provisioner.go` differ by **zero structural lines**. Every
difference is data: which fields the request carries (`ServerDesc`,
`RESTPort`, `RCONPort` vs `OwnerID`, `WorldName`), which env vars they
become, and the port-shape rule (Palworld's four distinct ports vs
Dragonwilds' pair plus agent). Container creation, ownership labels, data
directories, discovery and destroy are identical.

Divergence across the other shared packages, same method:

| package | lines | differing |
|---|---|---|
| `crypto`, `db`, `savecache` | 637 | 0% |
| `store` | 2213 | 1% |
| `agentfiles` | 161 | 4% |
| `sched` | 995 | 8% |
| `dockerctl`, `agentctl`, `notify` | 2555 | 13–14% |
| `backup` | 1012 | 19% |
| `steamcmd` | 98 | 27% |

So the plan is: one **host provisioner** service (its own repo, its own
image, one per host), and a **shared library** for the packages above. The
game agents (`supervisor`, `files`, `bridge`, launch profiles) stay per
game — that is where the divergence actually lives, and it is real
divergence, not drift.

**The contract is profile-as-data, and it now exists** (`internal/wkagent/
spec.go`): the console sends image, env, ports, slug and mount; the
provisioner places the container and never learns what a "world name" is. A
test places a *Palworld-shaped* server through the Dragonwilds
provisioner, which is the evidence the boundary is real rather than
aspirational. `/v1/provision` is now one caller of that path, not a second
implementation.

Two constraints are load-bearing and must survive the extraction, because a
generic "place a container" verb is otherwise an arbitrary-code-execution
primitive for whoever holds the token:

1. **Image allowlist.** Prefix-checked, defaulting to the project's own
   registry namespace. A leaked token deploys a newer agent, not a payload.
2. **No caller-controlled host paths.** The caller names a slug; the
   provisioner decides the directory under its data root. There is
   deliberately no bind-mount field.

Order of work, so the risky part comes last:

1. ~~Define the spec and route the existing handler through it.~~ **Done.**
2. Host-port awareness across *all* containers (item 6 above) — the palcon
   collision fix, and the first thing the shared service must do that
   neither agent does today.
3. Lift `internal/wkagent`'s provisioner mode into its own repo, with the
   spec as its API. wkagent keeps supervisor and companion modes.
4. Lift the 0–8% packages into the shared library; leave the 13–27% ones
   alone until their divergence is understood — `steamcmd` and `backup`
   differ for game reasons, not accidental drift.

Migration note for step 3: existing containers carry
`wildskeeper.provisioned` / `wildskeeper.slug`, and every destroy and
recreate gate reads them. A neutral namespace means recognising both for a
release, not relabelling live containers.

What this trades away: independent deployability. Today each console owns
its own provisioner and neither can break the other. A shared service is
stronger coupling than a shared library — a version-skewed provisioner
breaks a *running* console, and one down means neither can provision. That
is the cost being accepted for one host view and one implementation, and
it is why the wire contract wants to be stable before the split rather
than after.
