--[[
  dwbridge — the command bridge Dragonwilds has no native protocol for.

  RuneScape: Dragonwilds ships no RCON, REST, or query interface, so the
  Wildskeeper console derives everything it can from logs and files. The one
  thing it cannot derive is *action*: save-now, kick, ban, broadcast. This
  UE4SS Lua mod is that missing action channel.

  Transport is the filesystem, because that is the one thing the wkagent
  sidecar and this mod reliably share (a bind-mounted volume in the docker
  deployment, the install tree locally). No sockets, no ports: the game runs
  under Wine and opening a listener from Lua across that boundary is far more
  fragile than a directory both sides can see.

  Protocol (v1), all files in DWBRIDGE_DIR. It is single-flight — the agent
  serializes commands, so exactly one is in flight — which lets the files
  have fixed names and the mod avoid directory listing entirely (io.popen
  'dir' is unreliable under Wine, and a listing race is one more thing to
  get wrong):
    heartbeat.json    written by the mod every ~2s: liveness + the command
                      list this build actually implements.
    request.json      written by the agent: {"id","command","args"{}}.
    response.json     written by the mod: {"id","ok","error","data"{}}.
  The mod claims a request by deleting request.json before it runs, so a
  slow handler can't be re-run, and correlates by echoing the request id so
  the agent never reads a stale response from a timed-out command.

  The bridge directory comes from the DWBRIDGE_DIR environment variable,
  which the launch profile sets to the same directory the agent uses (see
  docs/sidecar-agent.md). With it unset the mod loads but stays idle, so a
  server without the console is not forced to carry a bridge.
]]

local POLL_MS = 500
local HEARTBEAT_MS = 2000
local PROTOCOL = 1
local VERSION = "dwbridge/0.1"

-- Commands this build implements. The agent reads this list from the
-- heartbeat and only routes what appears here, so shipping a new verb is a
-- matter of adding its handler and its name — never a version handshake.
local COMMANDS = { "ping", "save" }

local dir = os.getenv("DWBRIDGE_DIR")

local function log(msg)
    print("[dwbridge] " .. msg .. "\n")
end

-- Path join that tolerates either separator: DWBRIDGE_DIR arrives Windows-
-- shaped under Wine (Z:\...\dwbridge) but the mod may also be run natively.
local sep = (dir and dir:find("\\")) and "\\" or "/"
local function path(name)
    return dir .. sep .. name
end

----------------------------------------------------------------------
-- Minimal JSON. The wire shapes are deliberately flat — objects of
-- string/number/bool, plus one string array — so a full parser is
-- unnecessary and a hand-rolled one has no dark corners.
----------------------------------------------------------------------

local function jsonEscape(s)
    return (s:gsub('[%z\1-\31\\"]', function(c)
        local map = { ['"'] = '\\"', ['\\'] = '\\\\', ['\n'] = '\\n', ['\r'] = '\\r', ['\t'] = '\\t' }
        return map[c] or string.format('\\u%04x', string.byte(c))
    end))
end

local function jsonValue(v)
    local t = type(v)
    if t == "string" then
        return '"' .. jsonEscape(v) .. '"'
    elseif t == "number" then
        -- Positions are floats; %d on those would throw in Lua 5.4. Two
        -- decimals is centimetre precision in UE units — plenty for a map.
        if v == math.floor(v) and v == v and v ~= math.huge and v ~= -math.huge then
            return string.format("%d", v)
        end
        return string.format("%.2f", v)
    elseif t == "boolean" then
        return v and "true" or "false"
    elseif t == "table" then
        -- array of strings (COMMANDS) — the only array shape emitted
        local parts = {}
        for _, item in ipairs(v) do
            parts[#parts + 1] = jsonValue(item)
        end
        return "[" .. table.concat(parts, ",") .. "]"
    end
    return "null"
end

-- encodeObject keeps a caller-supplied key order so output is stable
-- (stable files diff cleanly and read predictably in a log).
local function encodeObject(order, obj)
    local parts = {}
    for _, k in ipairs(order) do
        if obj[k] ~= nil then
            parts[#parts + 1] = '"' .. k .. '":' .. jsonValue(obj[k])
        end
    end
    return "{" .. table.concat(parts, ",") .. "}"
end

-- decodeFlat pulls the fields the request protocol defines out of a blob,
-- without a general parser: "command" is a top-level string, and "args" is
-- a flat object whose string values are read on demand by handlers. This is
-- intentionally forgiving — an unparseable request becomes command=nil and
-- is rejected, never a Lua error that would stall the poll loop.
local function decodeString(blob, key)
    local pat = '"' .. key .. '"%s*:%s*"(.-)"'
    local v = blob:match(pat)
    if not v then return nil end
    -- unescape the escapes jsonEscape produces
    v = v:gsub('\\n', '\n'):gsub('\\r', '\r'):gsub('\\t', '\t'):gsub('\\"', '"'):gsub('\\\\', '\\')
    return v
end

-- argString reads args.<key> from the request blob. The args object is
-- isolated first so a key that also appears at the top level can't leak in.
local function argString(blob, key)
    local argsBlob = blob:match('"args"%s*:%s*(%b{})')
    if not argsBlob then return nil end
    return decodeString(argsBlob, key)
end

----------------------------------------------------------------------
-- File helpers
----------------------------------------------------------------------

local function readFile(p)
    local f = io.open(p, "rb")
    if not f then return nil end
    local data = f:read("*a")
    f:close()
    return data
end

-- writeFileAtomic writes to a .tmp sibling then renames, so a reader never
-- sees a torn file. Windows (and therefore Wine) rename does NOT overwrite
-- an existing destination — it fails and leaves the .tmp behind — so the
-- destination is removed first. That opens a sub-millisecond window where
-- the file is absent; for the heartbeat that only costs one poll (the agent
-- re-reads within 100 ms), and response files have unique names so the
-- remove is a harmless no-op. This is the one place the Linux and Wine
-- filesystem semantics genuinely differ, and getting it wrong strands the
-- heartbeat at its first value.
local function writeFileAtomic(p, data)
    local tmp = p .. ".tmp"
    local f = io.open(tmp, "wb")
    if not f then return false end
    f:write(data)
    f:close()
    os.remove(p)
    os.rename(tmp, p)
    return true
end

----------------------------------------------------------------------
-- Command handlers. Each returns ok:boolean, errorMessage:string|nil,
-- data:table|nil. A handler must never raise — the caller pcall-wraps it,
-- but returning a clean error keeps the response useful.
----------------------------------------------------------------------

local handlers = {}

function handlers.ping(_)
    -- Liveness with a real object touch: proves the mod is not just loaded
    -- but attached to a running world, which is what "bridge available"
    -- must actually mean before the agent routes a mutating command.
    local gm = FindFirstOf("GameModeBase")
    local alive = gm ~= nil and gm:IsValid()
    return true, nil, { world = alive }
end

function handlers.save(_)
    local ps = FindFirstOf("PersistenceSubsystem")
    if not (ps and ps:IsValid()) then
        return false, "PersistenceSubsystem not found (is a world loaded?)", nil
    end
    -- Verified headless: this is the same call the game makes on autosave,
    -- and it writes the world with no player connected. The bool is
    -- bAdditionalLogging — on, so the save is visible in the game log the
    -- agent already tails.
    ps:SaveGame(true)
    return true, nil, nil
end

----------------------------------------------------------------------
-- Request loop
----------------------------------------------------------------------

local REQUEST = "request.json"
local RESPONSE = "response.json"

-- poll handles at most one pending request per tick. It reads request.json,
-- deletes it immediately (claiming it, so a slow handler is never re-run),
-- runs the command, and writes response.json echoing the request id.
local function poll()
    local reqPath = path(REQUEST)
    local blob = readFile(reqPath)
    if not blob then return end
    os.remove(reqPath) -- claim before running

    local id = decodeString(blob, "id") or ""
    local command = decodeString(blob, "command")
    local ok, errMsg, data
    if not command then
        ok, errMsg = false, "request had no command"
    else
        local handler = handlers[command]
        if not handler then
            ok, errMsg = false, "unknown command: " .. command
        else
            local pok, r1, r2, r3 = pcall(handler, blob)
            if pok then
                ok, errMsg, data = r1, r2, r3
            else
                ok, errMsg = false, "handler error: " .. tostring(r1)
            end
        end
    end

    -- data, when present, is a nested object the flat encoder doesn't cover,
    -- so it's assembled by hand; it stays the {world=bool} shape ping uses.
    local body
    if data ~= nil then
        local inner = {}
        for k, v in pairs(data) do
            inner[#inner + 1] = '"' .. k .. '":' .. jsonValue(v)
        end
        body = string.format('{"id":"%s","ok":%s%s,"data":{%s}}',
            id, ok and "true" or "false",
            errMsg and (',"error":"' .. jsonEscape(errMsg) .. '"') or "",
            table.concat(inner, ","))
    else
        local resp = { id = id, ok = ok }
        if errMsg then resp.error = errMsg end
        body = encodeObject({ "id", "ok", "error" }, resp)
    end

    writeFileAtomic(path(RESPONSE), body)
    log(string.format("%s -> ok=%s%s", command or "?", tostring(ok), errMsg and (" (" .. errMsg .. ")") or ""))
end

local function writeHeartbeat()
    writeFileAtomic(path("heartbeat.json"),
        string.format('{"ts":%d,"version":"%s","protocol":%d,"commands":%s}',
            os.time(), VERSION, PROTOCOL, jsonValue(COMMANDS)))
end

----------------------------------------------------------------------
-- Live state (state.json): the read-only telemetry channel.
--
-- Published on the heartbeat cadence rather than fetched by command:
-- periodic state is what a polling file transport is *best* at, and it
-- costs the agent nothing to read. Everything here is property reads on
-- replicated engine objects — the safe side of the feasibility line (the
-- recon doc's rule: never call native UFunctions). Every read is
-- pcall-guarded per player and per field, so a patch that renames one
-- property degrades that field, not the roster and never the mod.
----------------------------------------------------------------------

-- collectPlayers walks GameState.PlayerArray — the engine's replicated
-- roster, present on every UE server — and reads name and pawn position.
-- The root component's RelativeLocation IS world location for a root, and
-- is a plain struct property (K2_GetActorLocation would be a native call).
local function collectPlayers()
    local players = {}
    local gs = FindFirstOf("GameStateBase")
    if not (gs and gs:IsValid()) then return players end
    local ok, arr = pcall(function() return gs.PlayerArray end)
    if not ok or not arr then return players end
    local n = 0
    pcall(function() n = #arr end)
    for i = 1, n do
        local pok, entry = pcall(function()
            local ps = arr[i]
            if not (ps and ps:IsValid()) then return nil end
            local e = {}
            local nok, name = pcall(function() return ps.PlayerNamePrivate:ToString() end)
            if nok and name and name ~= "" then e.name = name end
            local wok, pawn = pcall(function() return ps.PawnPrivate end)
            if wok and pawn and pawn:IsValid() then
                local lok, loc = pcall(function() return pawn.RootComponent.RelativeLocation end)
                if lok and loc then
                    e.x, e.y, e.z = loc.X, loc.Y, loc.Z
                end
            end
            return e
        end)
        -- A player we can't even name is omitted rather than sent blank.
        if pok and entry and entry.name then players[#players + 1] = entry end
    end
    return players
end

-- collectWorld tries the in-game clock. The subsystem class is known from
-- the server log (DominionInGameTimeSubsystem); its property names are not,
-- so this probes a shortlist and omits what isn't there. Fields appearing
-- in state.json after a real run is how the right names get confirmed.
local function collectWorld()
    local w = {}
    local ts = FindFirstOf("DominionInGameTimeSubsystem")
    if not (ts and ts:IsValid()) then return w end
    for field, candidates in pairs({
        day = { "CurrentDay", "Day", "DayNumber", "DayCount" },
        timeOfDay = { "CurrentTimeOfDay", "TimeOfDay", "CurrentTime", "InGameTime" },
    }) do
        for _, prop in ipairs(candidates) do
            local ok, v = pcall(function() return ts[prop] end)
            if ok and type(v) == "number" then
                w[field] = v
                break
            end
        end
    end
    return w
end

local function writeState()
    local players = collectPlayers()
    local parts = {}
    for _, p in ipairs(players) do
        parts[#parts + 1] = encodeObject({ "name", "x", "y", "z" }, p)
    end
    local world = collectWorld()
    local worldJSON = encodeObject({ "day", "timeOfDay" }, world)
    writeFileAtomic(path("state.json"),
        string.format('{"ts":%d,"players":[%s],"world":%s}',
            os.time(), table.concat(parts, ","), worldJSON))
end

----------------------------------------------------------------------
-- Boot
----------------------------------------------------------------------

if not dir then
    log("DWBRIDGE_DIR not set; bridge idle (no console attached)")
    return
end

log("bridge dir: " .. dir)
writeHeartbeat()

-- Two independent timers: a fast poll for requests, a slower heartbeat.
-- LoopAsync's callback returning false keeps it looping.
LoopAsync(POLL_MS, function()
    local pok, err = pcall(poll)
    if not pok then log("poll error: " .. tostring(err)) end
    return false
end)

LoopAsync(HEARTBEAT_MS, function()
    pcall(writeHeartbeat)
    -- State rides the same timer but its own pcall: a bad object walk must
    -- never cost the heartbeat, which is what "the bridge is up" means.
    pcall(writeState)
    return false
end)

log("started; commands: " .. table.concat(COMMANDS, ", "))
