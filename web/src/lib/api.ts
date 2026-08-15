export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

/** Registered by the auth provider. A 401 from any endpoint means the
 * session expired (or was revoked) mid-use; clearing auth state here lets
 * RequireAuth bounce to /login once instead of every query surfacing its
 * own scattered error. */
let onUnauthorized: (() => void) | null = null;

export function setUnauthorizedHandler(handler: (() => void) | null) {
  onUnauthorized = handler;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api${path}`, {
    ...init,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...init?.headers,
    },
  });

  if (res.status === 204) {
    return undefined as T;
  }

  const isJSON = res.headers.get("content-type")?.includes("application/json");
  const body = isJSON ? await res.json() : undefined;

  if (!res.ok) {
    // /login's own 401 is just a wrong password, not an expired session.
    if (res.status === 401 && path !== "/login") {
      onUnauthorized?.();
    }
    const message = body && typeof body === "object" && "error" in body ? String(body.error) : res.statusText;
    throw new ApiError(res.status, message);
  }
  return body as T;
}

/** The server's explanation for a failed request — for toasts that would
 * otherwise say what failed but not why. */
export function errorDetail(err: unknown): string | undefined {
  return err instanceof ApiError && err.message ? err.message : undefined;
}

export const PERMISSIONS = ["power", "broadcast", "save", "moderate", "shutdown", "settings"] as const;
export type Permission = (typeof PERMISSIONS)[number];

/** Human labels for the permission checkboxes, and what each actually allows. */
export const PERMISSION_LABELS: Record<Permission, { label: string; help: string }> = {
  power: { label: "Power", help: "Start, stop and restart the server container, and repair or update its install" },
  broadcast: { label: "Broadcast", help: "Send in-game messages" },
  save: { label: "Save world", help: "Trigger a world save" },
  moderate: { label: "Moderate", help: "Kick, ban and unban players" },
  shutdown: { label: "In-game shutdown", help: "Shut the server down with a countdown" },
  settings: { label: "Edit settings", help: "Read and edit DedicatedServer.ini" },
};

export interface Me {
  username: string;
  role: string;
  isAdmin: boolean;
  permissions: Permission[];
}

export interface AppUser {
  id: number;
  username: string;
  role: string;
  permissions: Permission[];
  disabled: boolean;
}

export interface UserWriteInput {
  username?: string;
  password?: string;
  role: string;
  permissions: Permission[];
  disabled?: boolean;
}

export interface ContainerState {
  name: string;
  status: string;
  running: boolean;
  startedAt: string;
  exitCode: number;
}

export interface Server {
  id: number;
  name: string;
  /** Which game this server runs ("palworld"). Picks the per-game labels in
   * lib/games — the rest of the console is game-agnostic. */
  game: string;
  /** Views this server's game can fill, in nav order. Distinct from
   * hiddenFeatures: this is what exists, that is what an admin switched off. */
  features: Feature[];
  host: string;
  rconPort: number;
  hasRconPassword: boolean;
  restPort: number;
  hasRestPassword: boolean;
  /** UDP port players join on — display metadata. */
  gamePort: number;
  /** Public address players outside the LAN connect to; empty = use host. */
  joinAddress: string;
  useRest: boolean;
  enabled: boolean;
  savePath: string;
  configPath: string;
  installPath: string;
  agentUrl: string;
  hasAgentToken: boolean;
  containerName: string;
  /** Views an admin has switched off for this server, so the nav knows what to
   * leave out. Names the hidden views only — admins still get their data. */
  hiddenFeatures: string[];
}

/** Views that can be switched off per server. The keys are also the route
 * segments, so one string drives the nav link and the API's refusal. */
export const FEATURES = [
  "map",
  "pals",
  "inventory",
  "storage",
  "paldex",
  "achievements",
  "guilds",
  "calculators",
  "saves",
  "logs",
] as const;
export type Feature = (typeof FEATURES)[number];

/** Kinds of data a single player can be withheld from. Coarser than the view
 * list on purpose: Player pals, Paldex and Calculators all read one payload,
 * so they share one switch. */
export const STREAMS = ["pals", "inventory", "map"] as const;
export type Stream = (typeof STREAMS)[number];

export interface VisibilityResult {
  hiddenFeatures: string[];
  hidePrivateStorage: boolean;
  /** player uid -> streams that player is withheld from. */
  players: Record<string, string[]>;
  roster: { uid: string; nickname: string; level: number }[];
  /** True when the roster is empty because the save couldn't be read, rather
   * than because nobody has played. */
  rosterUnavailable: boolean;
  allFeatures: string[];
  allStreams: string[];
}

export interface VisibilityInput {
  hiddenFeatures: string[];
  /** Whether the Storage view may search password-locked chests. */
  hidePrivateStorage: boolean;
  players: Record<string, string[]>;
}

export interface ServerWriteInput {
  name: string;
  host: string;
  gamePort: number;
  joinAddress: string;
  enabled: boolean;
  savePath: string;
  configPath: string;
  installPath: string;
  agentUrl: string;
  /** Empty keeps the stored token (like password updates). */
  agentToken?: string;
  containerName: string;
}

/** One background job on a server's wkagent sidecar. */
export interface SteamJob {
  id: string;
  kind: string;
  state: "running" | "done" | "failed";
  startedAt: string;
  finishedAt?: string;
  error?: string;
  log?: string[];
}

export interface SteamUpdateStatus {
  /** Running job, else the last finished one, else null. */
  job: SteamJob | null;
  agent: {
    version: string;
    apiVersion: number;
    mode: string;
    installDirOk: boolean;
    diskFreeBytes: number;
  };
}

export interface ProvisionInput {
  name: string;
  host: string;
  dataPath: string;
  /** Published UDP port. The game also uses the port above it, and both
   * are published — so a proposal has to keep the pair free. */
  gamePort: number;
  agentPort: number;
  imageTag: string;
  /** The Player ID that owns the server. Required: the game refuses to
   * start without one. */
  ownerId: string;
  /** Blank = generated server-side. */
  adminPassword?: string;
  /** In-game ServerName; blank = the dashboard display name. */
  serverName?: string;
  /** Names the world created on the server's first boot. */
  worldName?: string;
  /** Container user:group; blank = 568:568, "root" = image default. */
  runAs?: string;
}

export interface ProvisionResult {
  server: Server;
  adminPassword: string;
  agentToken: string;
  /** The complete per-server compose stack to paste and deploy. */
  stack: string;
  /** True when a provisioner deployed the stack already — no paste needed. */
  deployed: boolean;
  /** Set when a provisioner was configured but the deploy failed. */
  deployError?: string;
  /** Where the provisioner put the data (provisioner deploys only). */
  dataDir?: string;
}

/**
 * Undefined unless the container was destroyed too — a plain row deletion
 * answers 204 with no body.
 */
export type DeleteServerResult = { destroyed: string; dataDir?: string } | undefined;

/** What the wizard can prefill from the provisioner's configuration. */
export interface ProvisionDefaults {
  available: boolean;
  host?: string;
  dataRoot?: string;
  runAs?: string;
  imageTag?: string;
  ports?: { game: number; agent: number };
}

/** A wkagent container found on the provisioner's host. */
export interface DiscoveredServer {
  name: string;
  image: string;
  mode: string;
  running: boolean;
  gamePort?: number;
  agentPort?: number;
  /** Already registered here (matched by agent port). */
  registered: boolean;
}

/**
 * Which of Dragonwilds' two dedicated-server builds the agent launches.
 *
 * Not a preference: the native Linux build cannot load UE4SS, so it can
 * never carry the dwbridge mod and its commands stay unavailable forever.
 * The Windows build under Wine can. The two also come from different Steam
 * depots, so switching means re-downloading the game — which is why
 * `installed` is per profile and worth showing.
 */
export interface Launch {
  profile: string;
  label: string;
  /** Whether this build can carry the mod, and so run commands at all. */
  mods: boolean;
  /** Whether the launcher exists on this agent at all. False for the Wine
   * build on an agent image with no Wine in it — a different problem from
   * "not installed", fixable only by moving the agent to another image. */
  runnable: boolean;
  /** Whether the selected build's files are present. False between a switch
   * and the re-install it needs. */
  installed: boolean;
  /** Profiles the console may select. Empty when the agent runs an explicit
   * command, which must not be silently replaced. */
  available?: string[];
  /** The selection has changed since the running process started. */
  pendingRestart: boolean;
  configPath: string;
  /** The agent's image carries the UE4SS kit, so one-click install exists. */
  bridgeKit?: boolean;
  /** A UE4SS install already sits next to the exe. */
  bridgeInstalled?: boolean;
}

export const LAUNCH_PROFILES: Record<string, { label: string; blurb: string }> = {
  native: { label: "Native Linux", blurb: "Simplest to run. No mod support, so commands stay unavailable." },
  wine: { label: "Windows + mods", blurb: "Runs under Wine and can load the dwbridge mod, so on-demand saves work." },
};

/** One command's answer from the capabilities probe. */
export interface CommandCapability {
  supported: boolean;
  /** Why not, when unsupported — the same text the 501 would carry. Shown
   * verbatim, so it should name what's missing. */
  reason?: string;
}

/**
 * What a server's commands can actually do right now.
 *
 * For Dragonwilds the answer moves with the dwbridge mod, which is a
 * property of the machine rather than the game — so it has to be asked, not
 * assumed. `probed` is false for a game whose client can't answer; every
 * command then reports supported, which is what the UI assumed before this
 * existed. Treat a failed request the same way: show the control and let a
 * 501 explain itself, rather than hiding a working button.
 */
export interface Capabilities {
  probed: boolean;
  commands: Record<string, CommandCapability | undefined>;
}

export interface ServerInfo {
  servername: string;
  version: string;
  playerCount: number;
  /** Which transport answered. Always "agent" for Dragonwilds — the game
   * has no query protocol of its own. */
  transport: string;
}

export interface Player {
  name: string;
  playerId: string;
  userId: string;
  level: number;
  ping: number;
  location_x: number;
  location_y: number;
}

export interface Metrics {
  serverfps: number;
  serverframetime: number;
  currentplayernum: number;
  maxplayernum: number;
  uptime: number;
  days: number;
}

export type Settings = Record<string, unknown>;

/** One editable PalWorldSettings.ini option. `type` decides which control the
 * editor renders; `value` is the decoded display value (strings unquoted). */
export interface ConfigSetting {
  key: string;
  value: string;
  type: "bool" | "int" | "float" | "string" | "enum";
  /** The ini [section] the key sits under, for games with sectioned files
   * (Dragonwilds). Display only — settings are addressed by key. */
  section?: string;
}

export interface ConfigResult {
  settings: ConfigSetting[];
  /** Resolved path of the PalWorldSettings.ini that was read. */
  path: string;
  /** False when the config file is on a read-only mount — edits will fail. */
  writable: boolean;
}

/** One collected sample. Nulls are real gaps — the server was unreachable
 * or reported nothing — and must break the line rather than plot as zero. */
export interface MetricPoint {
  ts: string;
  playerCount: number | null;
  maxPlayers: number | null;
  serverFps: number | null;
  frameTime: number | null;
}

export interface MetricsHistory {
  points: MetricPoint[];
  /** Collection cadence, so the chart can tell a gap from sparse sampling. */
  intervalSeconds: number;
}

export interface Pal {
  instanceId: string;
  characterId: string;
  nickname: string;
  level: number;
  gender: "male" | "female" | "";
  isBoss: boolean;
  isLucky: boolean;
  rank: number;
  talentHp: number;
  talentShot: number;
  talentDefense: number;
  passives: string[];
  exp: number;
  skills: string[];
  hp: number;
  sanity: number;
  stomach: number;
  friendship: number;
  /** Ailment name, or "" when healthy. A sick pal stops working at a base. */
  sick: string;
  /** Soul upgrades applied, keyed by stat name. */
  souls: Record<string, number>;
  /** Work-book enhancements from the save: suitability name -> ranks added.
   * The condenser's star bonus is NOT here — the game derives it from rank
   * at runtime, and lib/crew.ts does the same. */
  workAdds?: Record<string, number>;
  /** Suitabilities the player switched off for this pal: levels it has,
   * jobs it won't take. */
  workOff?: string[];
  slotIndex: number;
  /** The base camp a working pal belongs to (matches a guild base's id);
   * empty for pals not working at a base. */
  baseId: string;
}

/** One occupied slot of a player's bag. `slot` is its real position in the
 * container's grid — the save keeps it, so gaps in a bag are real gaps. */
export interface ItemSlot {
  slot: number;
  itemId: string;
  count: number;
  /** Present only on items that carry per-instance state: gear has
   * durability (guns a round count), eggs name the species inside. */
  durability?: number;
  ammo?: number;
  eggSpecies?: string;
  passives?: string[];
}

/** One of a player's bags. `size` is its capacity, so an unfilled slot reads
 * differently from one the player hasn't unlocked. */
export interface ItemContainer {
  size: number;
  slots: ItemSlot[];
}

/** A player's containers keyed by role: common (backpack), essential (key
 * items), weapons, equipment, food, drop. */
export type Inventory = Record<string, ItemContainer>;

/**
 * The player's own save entry. Carries no derived totals on purpose: the
 * Health/Attack/Defense/Work Speed figures the game's character screen shows
 * are computed at runtime from base values, level and gear, and the save
 * records none of them. `hp` and `shield` are current values for the same
 * reason — their maxima never reach the file.
 */
export interface Character {
  exp: number;
  hp: number;
  shield: number;
  /** A real percentage, unlike a pal's species-dependent stomach. */
  stomach: number;
  unusedStatusPoints: number;
  /** Points spent on level-up, and the scarcer "Ex" pool, by stat name. */
  statusPoints: Record<string, number>;
  exStatusPoints: Record<string, number>;
  /** The food buff running when the save was written, and its time left. */
  foodBuff: string;
  foodBuffSeconds: number;
}

export interface PlayerInventory {
  uid: string;
  nickname: string;
  level: number;
  inventory: Inventory;
  /** Absent for a uid that owns bags but has no player entry in the save. */
  character?: Character;
  /** Unix seconds; 0 when the save recorded none. A *login* stamp — see
   * PlayerPals.lastOnline. */
  lastOnline: number;
  /** Unix seconds; 0 when wildskeeper never watched this player leave. */
  lastSeen: number;
  platform: string;
}

export interface PlayerPals {
  uid: string;
  nickname: string;
  level: number;
  party: Pal[];
  palbox: Pal[];
  base: Pal[];
  /** Dimensional Pal Storage, plus this player's share of the global storage. */
  storage: Pal[];
  /** Present only in the bundled demo fixture, which is a whole-save capture.
   * The live /pals and /guilds endpoints deliberately omit these two: three
   * views read that payload, and shipping bags and character sheets there
   * would route around the Inventory view's own visibility switches. Read them
   * from /inventory, which honours them. */
  inventory?: Inventory;
  character?: Character;
  /** Unix seconds; 0 when the save recorded none. This is the save's own
   * LastOnlineDateTime, which Palworld writes when a player *connects* and
   * never updates — so for anyone offline it says when they arrived, not when
   * they left. Use lastSeen for "last seen"; this is only its fallback. */
  lastOnline: number;
  /** Unix seconds; wildskeeper's own observation of this player leaving, and 0
   * when it has none (a server it hasn't collected for, or history predating
   * the record). Unlike lastOnline this really is a last-seen time. */
  lastSeen: number;
  /** Where they logged off, in the same world space the map plots. */
  lastX: number | null;
  lastY: number | null;
  platform: string;
  technologyPoints: number;
  /** Paldex progress: registered species ids (survive selling the pal) and
   * per-species sphere-capture counts, from the player's save record. */
  paldeck: string[];
  captures: Record<string, number>;
}

export interface PlayerEvent {
  id: number;
  ts: string;
  userId: string;
  name: string;
  event: "join" | "leave";
}

export interface ActivityResult {
  events: PlayerEvent[];
  hours: number;
  /** Sampling cadence — session edges are only this precise. */
  intervalSeconds: number;
}

export interface AuditEntry {
  id: number;
  ts: string;
  username: string;
  action: string;
  detail: string;
}

export interface RestartSchedule {
  id: number;
  enabled: boolean;
  /** Weekdays, 0 (Sunday) through 6 (Saturday). */
  days: number[];
  /** "HH:MM", 24-hour, in Wildskeeper's local timezone. */
  timeOfDay: string;
  /** Warning broadcast lead times in minutes, descending. */
  warningMinutes: number[];
  lastRunAt: string | null;
  nextRunAt: string | null;
}

export interface ScheduleWriteInput {
  enabled: boolean;
  days: number[];
  timeOfDay: string;
  warningMinutes: number[];
}

/** The webhook URL itself is write-only; the API only ever reports that
 * one is configured. */
export interface DiscordConfig {
  configured: boolean;
  enabled: boolean;
  onStatus: boolean;
  onPlayers: boolean;
  onRestarts: boolean;
}

export interface DiscordWriteInput {
  /** Empty string keeps the stored webhook (like password updates). */
  webhookUrl: string;
  enabled: boolean;
  onStatus: boolean;
  onPlayers: boolean;
  onRestarts: boolean;
}

export interface AutomationResult {
  schedules: RestartSchedule[];
  /** Wildskeeper's local timezone name, which schedule times are read in. */
  timezone: string;
  /** True when a scheduled restart can bounce the container itself. */
  dockerRestart: boolean;
  /** Absent for non-admins. */
  discord?: DiscordConfig;
  /** Absent for non-admins. `available` = docker control + container name. */
  /** `supervised` means a wkagent owns the game process, which is why
   * `available` is false: its own supervisor already does this job. */
  watchdog?: { enabled: boolean; available: boolean; supervised?: boolean };
  /** Absent for non-admins. Token is the /status/<token> URL segment. */
  publicStatus?: { enabled: boolean; token: string };
}

export interface BackupSnapshot {
  name: string;
  ts: string;
  bytes: number;
}

/** World metadata parsed from the server's save file (Dragonwilds SPUD). */
export interface WorldInfo {
  worldName: string;
  mapName: string;
  /** Rendered as the server logs it (WorldSaveGuid, uppercase hex). */
  saveGuid: string;
  version: number;
  /** Bumped by the game on every save — the autosave odometer. */
  saveFileRevision: number;
  friendlyFire: boolean;
  survivalDifficulty: number;
  hardcoreState: number;
  crossplayEnabled: boolean;
  sessionPrivacy: number;
  hasSessionPassword: boolean;
  ownerId: string;
  ownerName: string;
  lastSavedBy: string;
  headerStamp: string;
  timeOfSave: string;
  levels: string[];
  chunks: { id: string; bytes: number }[];
  file: string;
  /** When the save file was last written — the trustworthy freshness stamp. */
  modTime: string;
}

export interface WorldResult {
  /** False when nothing can be read: no save path, or a game with no reader. */
  available: boolean;
  world?: WorldInfo;
}

export interface BackupsResult {
  /** False when the server has no save path to snapshot. */
  available: boolean;
  running: boolean;
  /** 0 = no schedule; manual backups still work. */
  intervalHours: number;
  keep: number;
  snapshots: BackupSnapshot[];
  totalBytes: number;
}

/** The unauthenticated status snapshot behind a public token. */
export interface PublicStatus {
  name: string;
  online: boolean;
  players?: number;
  maxPlayers?: number;
  nextRestartAt?: string;
}

export interface GuildMember {
  uid: string;
  name: string;
}

export interface Guild {
  id: string;
  name: string;
  baseCampLevel: number;
  members: GuildMember[];
  memberCount: number;
  bases: { id: string; x: number; y: number }[];
}

export interface GuildsResult {
  guilds: Guild[];
  players: PlayerPals[];
  parsedAt: string;
  saveModTime: string;
}

export interface PalsResult {
  players: PlayerPals[];
  guilds: Guild[];
  parsedAt: string;
  saveModTime: string;
}

/** The model asking the browser to run one calculator tool. `signature` is
 * provider bookkeeping (Gemini's thought signature, base64) that must ride
 * the round trip untouched — echo the whole object back verbatim. */
export interface AdvisorToolCall {
  id?: string;
  name: string;
  args: Record<string, unknown>;
  signature?: string;
}

/** The browser's answer to one tool call. */
export interface AdvisorToolResult {
  id?: string;
  name: string;
  content: string;
}

/** A browser-implemented tool the model may call — defined next to the code
 * that executes it (lib/advisor-tools.ts) and forwarded by the server. */
export interface AdvisorTool {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

/** One turn of a pal-advisor conversation. The browser resends the whole
 * conversation with each question — the server keeps no chat state. Tool
 * turns exist because the calculators run here in the browser: an assistant
 * turn may carry the model's tool calls, answered by a "tool" turn. */
export interface AdvisorMessage {
  role: "user" | "assistant" | "tool";
  content?: string;
  toolCalls?: AdvisorToolCall[];
  toolResults?: AdvisorToolResult[];
}

/** One model turn: a final reply, or tool calls to execute and re-submit
 * (content then carries any preamble the model wrote before calling). */
export interface AdvisorChatResponse {
  reply?: string;
  content?: string;
  toolCalls?: AdvisorToolCall[];
}

/** Whether the advisor is available for THIS user, whose API answers their
 * questions, where that key came from (their personal key shadows the
 * shared one), and whether they may manage the shared key. The key itself
 * is never in any payload. */
/** One model a key's owner may pick. The list is served by the server so
 * the picker can never offer a model the server would reject. */
export interface AdvisorModelOption {
  id: string;
  label: string;
}

export interface AdvisorStatus {
  enabled: boolean;
  provider: string;
  /** The resolved model THIS user's questions run on. */
  model: string;
  source: "env" | "ui" | "personal" | "";
  canConfigure: boolean;
  hasPersonalKey: boolean;
  /** How many tool round-trips one question may take — the chat loop runs
   * in the browser, so the server hands the cap over. Admin-tunable. */
  maxToolRounds: number;
  /** Valid model choices per provider; first entry is the default. */
  modelOptions: Record<string, AdvisorModelOption[]>;
}

export interface InventoryResult {
  players: PlayerInventory[];
  parsedAt: string;
  saveModTime: string;
}

/**
 * One player's completion record, from RecordData in their save file.
 *
 * The three flavours here are not comparable and must not be totalled
 * together: towers and quests are permanent, raids and the counters only
 * climb, and fieldBosses is respawn state the game periodically clears — so
 * it means "beaten since the last reset", not a lifetime tally.
 */
export interface PlayerRecords {
  /** BOSS_BATTLE_NAME_<x> keys — the faction leaders this player has beaten. */
  towers: string[];
  /** Keyed <x>_Normal / <x>_Hard, so one tower appears once per difficulty. */
  towerCounts: Record<string, number>;
  /** PalSummon_<pal id> → summons defeated. */
  raids: Record<string, number>;
  /** Field alphas and human bounty targets in one map; only some keys
   * resolve to a name, the rest are opaque spawner ids. */
  fieldBosses: string[];
  /** The game's own achievement tiers: PalDex_1..10, BossDefeat_1..3 etc. */
  npcRewards: string[];
  quests: string[];
  /** Raw totals with no known denominator — fastTravel counts more map
   * points than there are statues, so none of these are percentages. */
  fastTravel: number;
  areas: number;
  /** Every effigy picked up, of every kind. */
  relics: number;
  /** Effigies per kind, keyed CapturePower and friends; sums to relics. */
  effigyTypes: Record<string, number>;
  notes: number;
  campsConquered: number;
  dungeonsCleared: number;
  fixedDungeonsCleared: number;
  treasuresFound: number;
  tribesCaptured: number;
  mutations: number;
  bossTechPoints: number;
  /** The solo arena ladder, keyed Bronze..Master; the highest present is the
   * rank this player holds. */
  arenaRanks: Record<string, number>;
  /** Effigy rank per bonus, keyed CapturePower and friends. The
   * movement/utility ones are the same figures the inventory view shows as
   * adventure stats; capture power is unique to this map. */
  relicRanks: Record<string, number>;
  predatorsDefeated: number;
  oilrigsCleared: number;
  awakenings: number;
  /** The game's own story-finished flag. False in saves from before it
   * existed, which reads the same as not finished. */
  gameCleared: boolean;
}

export interface AchievementsPlayer {
  uid: string;
  nickname: string;
  level: number;
  records: PlayerRecords;
  lastOnline: number;
  lastSeen: number;
}

export interface AchievementsResult {
  players: AchievementsPlayer[];
  parsedAt: string;
  saveModTime: string;
}

/**
 * One searchable container in the world — a base's chest, a furnace's hopper,
 * a treasure box on a hillside. Player bags aren't here; they're /inventory.
 *
 * `kind` is where it stands: "base" at a guild's camp, "world" for the loot the
 * map places, "unplaced" for storage the save gives no position.
 */
export interface StorageContainer {
  id: string;
  /** "guild" is the guild chest: shared storage with no position, reached from
   * any of the guild's chests. */
  kind: "base" | "world" | "guild" | "unplaced";
  /** Someone put a password on this chest. The password itself is never read
   * out of the save, so this is all the page knows about it. */
  private?: boolean;
  /** Blueprint id of the object holding it; see lib/structures.ts. */
  objectId: string;
  size: number;
  slots: ItemSlot[];
  baseId?: string;
  guildId?: string;
  /** World coordinates, present together. Absent for unplaced storage. */
  x?: number;
  y?: number;
}

/** A base camp, named by the guild that owns it — a camp's own name in the
 * save is an internal placeholder. */
export interface StorageBase {
  id: string;
  guildId: string;
  guildName: string;
  /** Position in its guild's base list — the id the map marks it by. */
  index: number;
  x: number;
  y: number;
}

/** Just enough to label a guild's chest. Sent apart from `bases` because a
 * guild chest belongs to a guild without standing at any camp. */
export interface StorageGuild {
  id: string;
  name: string;
}

export interface StorageResult {
  containers: StorageContainer[];
  bases: StorageBase[];
  guilds: StorageGuild[];
  /** False unless the request asked for world loot, so the view can say what
   * it's leaving out rather than implying this is everything. */
  includesWorld: boolean;
  /** False when an admin has kept password-locked chests out of the index.
   * The page says so rather than quietly returning fewer results. */
  includesPrivate: boolean;
  parsedAt: string;
  saveModTime: string;
}

export const api = {
  login: (username: string, password: string) =>
    request<{ username: string }>("/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  logout: () => request<void>("/logout", { method: "POST" }),
  me: () => request<Me>("/me"),
  changeOwnPassword: (currentPassword: string, newPassword: string) =>
    request<void>("/me/password", { method: "POST", body: JSON.stringify({ currentPassword, newPassword }) }),

  listUsers: () => request<AppUser[]>("/users"),
  createUser: (input: UserWriteInput) => request<AppUser>("/users", { method: "POST", body: JSON.stringify(input) }),
  updateUser: (id: number, input: UserWriteInput) =>
    request<AppUser>(`/users/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteUser: (id: number) => request<void>(`/users/${id}`, { method: "DELETE" }),

  containerStatus: (id: number) => request<ContainerState>(`/servers/${id}/container`),
  containerAction: (id: number, action: "start" | "stop" | "restart") =>
    request<ContainerState>(`/servers/${id}/container/${action}`, { method: "POST" }),
  // Needs the power permission, like the actions beside it.
  containerLogs: (id: number, tail: number) =>
    request<{ lines: string[] }>(`/servers/${id}/container/logs?tail=${tail}`),
  // Empties steamapps/ and steam/packages/ under the install path so
  // SteamCMD re-downloads after a game update corrupts its cache. Needs the
  // power permission and a configured install path.
  clearSteamCache: (id: number) =>
    request<{ removed: number }>(`/servers/${id}/steam-cache/clear`, { method: "POST" }),
  // SteamCMD update via the server's wkagent sidecar. POST starts a job
  // on the agent (409 while the container runs or a job is in flight);
  // GET reports the running/last job so the UI can poll — and rediscover
  // an in-flight update after a reload.
  steamUpdateStart: (id: number) =>
    request<{ job: SteamJob }>(`/servers/${id}/steam/update`, {
      method: "POST",
      body: JSON.stringify({ validate: true }),
    }),
  steamUpdateStatus: (id: number) => request<SteamUpdateStatus>(`/servers/${id}/steam/update`),
  // Which game build the agent launches. Throws a 400 ApiError when the
  // server has no agent, or has one that doesn't run the game.
  serverLaunch: (id: number) => request<Launch>(`/servers/${id}/launch`),
  // Rebuild this server's agent container on another wkagent image,
  // through the provisioner that created it.
  recreateAgent: (id: number, imageTag: string) =>
    request<{ container: string; image: string; previousImage: string }>(`/servers/${id}/agent/image`, {
      method: "POST",
      body: JSON.stringify({ imageTag }),
    }),
  /** One-click mod support: the agent lays its baked-in UE4SS+dwbridge kit
   * next to the server exe. Only offered when the launch payload says the
   * kit exists and nothing is installed yet. */
  installBridge: (id: number) =>
    request<{ installed: boolean; restartRequired: boolean }>(`/servers/${id}/bridge/install`, {
      method: "POST",
    }),
  setServerLaunch: (id: number, profile: string) =>
    request<Launch>(`/servers/${id}/launch`, { method: "PUT", body: JSON.stringify({ profile }) }),
  setWatchdog: (id: number, enabled: boolean) =>
    request<{ enabled: boolean }>(`/servers/${id}/watchdog`, { method: "PUT", body: JSON.stringify({ enabled }) }),
  setPublicStatus: (id: number, enabled: boolean) =>
    request<{ enabled: boolean; token: string }>(`/servers/${id}/public`, {
      method: "PUT",
      body: JSON.stringify({ enabled }),
    }),
  publicStatus: (token: string) => request<PublicStatus>(`/public/status/${token}`),

  // The world as its save file tells it — admin-only, like the vault it sits above.
  getWorld: (id: number) => request<WorldResult>(`/servers/${id}/world`),

  // Save backups — admin-only end to end (a snapshot is the whole world).
  listBackups: (id: number) => request<BackupsResult>(`/servers/${id}/backups`),
  setBackupSettings: (id: number, intervalHours: number, keep: number) =>
    request<{ intervalHours: number; keep: number }>(`/servers/${id}/backups/settings`, {
      method: "PUT",
      body: JSON.stringify({ intervalHours, keep }),
    }),
  runBackup: (id: number) => request<void>(`/servers/${id}/backups/run`, { method: "POST" }),
  deleteBackup: (id: number, name: string) => request<void>(`/servers/${id}/backups/${name}`, { method: "DELETE" }),
  backupDownloadURL: (id: number, name: string) => `/api/servers/${id}/backups/${name}/download`,

  listServers: () => request<Server[]>("/servers"),
  // New-server wizard: registers a fully wired row and returns the
  // supervisor stack file to deploy (docs/sidecar-agent.md phase 4).
  provisionServer: (input: ProvisionInput) =>
    request<ProvisionResult>("/servers/provision", { method: "POST", body: JSON.stringify(input) }),
  provisionDefaults: () => request<ProvisionDefaults>("/servers/provision/defaults"),
  provisionDiscover: () =>
    request<{ available: boolean; servers: DiscoveredServer[] }>("/servers/provision/discover"),
  // One-click re-registration of a discovered container: the provisioner
  // recovers the token/password it originally injected.
  adoptServer: (container: string, host?: string) =>
    request<{ server: Server }>("/servers/adopt", {
      method: "POST",
      body: JSON.stringify({ container, host }),
    }),
  getServer: (id: number) => request<Server>(`/servers/${id}`),
  createServer: (input: ServerWriteInput) => request<Server>("/servers", { method: "POST", body: JSON.stringify(input) }),
  updateServer: (id: number, input: ServerWriteInput) =>
    request<Server>(`/servers/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  // removeContainer additionally asks the provisioner to destroy the
  // container — only possible for ones it created, and never touching the
  // world data, which stays in its host directory.
  deleteServer: (id: number, removeContainer = false) =>
    request<DeleteServerResult>(`/servers/${id}${removeContainer ? "?removeContainer=true" : ""}`, {
      method: "DELETE",
    }),

  serverInfo: (id: number) => request<ServerInfo>(`/servers/${id}/info`),
  serverPlayers: (id: number) => request<Player[]>(`/servers/${id}/players`),
  // What this server's commands can actually do, so the UI can say so
  // before anyone clicks. See Capabilities.
  serverCapabilities: (id: number) => request<Capabilities>(`/servers/${id}/capabilities`),
  broadcast: (id: number, message: string) =>
    request<void>(`/servers/${id}/broadcast`, { method: "POST", body: JSON.stringify({ message }) }),
  kick: (id: number, playerUid: string, message: string) =>
    request<void>(`/servers/${id}/kick`, { method: "POST", body: JSON.stringify({ playerUid, message }) }),
  ban: (id: number, playerUid: string, message: string) =>
    request<void>(`/servers/${id}/ban`, { method: "POST", body: JSON.stringify({ playerUid, message }) }),
  unban: (id: number, playerUid: string) =>
    request<void>(`/servers/${id}/unban`, { method: "POST", body: JSON.stringify({ playerUid }) }),
  save: (id: number) => request<void>(`/servers/${id}/save`, { method: "POST" }),
  shutdown: (id: number, waitSeconds: number, message: string) =>
    request<void>(`/servers/${id}/shutdown`, { method: "POST", body: JSON.stringify({ waitSeconds, message }) }),

  // REST-only — throws a 400 ApiError for servers configured RCON-only.
  serverSettings: (id: number) => request<Settings>(`/servers/${id}/settings`),

  // PalWorldSettings.ini editor (needs the "settings" permission). Throws a
  // 400 ApiError when the server has no config path configured.
  serverConfig: (id: number) => request<ConfigResult>(`/servers/${id}/config`),
  updateServerConfig: (id: number, changes: Record<string, string>) =>
    request<ConfigResult>(`/servers/${id}/config`, { method: "PUT", body: JSON.stringify({ changes }) }),
  // Writes a fresh random AdminPassword into the ini and returns it exactly
  // once. Dragonwilds' one real remote-admin lever: the game revokes every
  // password-session admin when it changes (applies on restart).
  rotateAdminPassword: (id: number) =>
    request<{ password: string }>(`/servers/${id}/config/rotate-admin-password`, { method: "POST" }),
  serverMetrics: (id: number) => request<Metrics>(`/servers/${id}/metrics`),
  serverMetricsHistory: (id: number, minutes: number) =>
    request<MetricsHistory>(`/servers/${id}/metrics/history?minutes=${minutes}`),

  // Save-file-backed (phase 5) — throws a 400 ApiError when the server has
  // no save path configured.
  serverPals: (id: number) => request<PalsResult>(`/servers/${id}/pals`),
  serverGuilds: (id: number) => request<GuildsResult>(`/servers/${id}/guilds`),
  serverInventory: (id: number) => request<InventoryResult>(`/servers/${id}/inventory`),

  serverAchievements: (id: number) => request<AchievementsResult>(`/servers/${id}/achievements`),
  // World loot is asked for explicitly: it's most of the payload, and most of
  // it is the location of chests nobody has opened yet.
  serverStorage: (id: number, world = false) =>
    request<StorageResult>(`/servers/${id}/storage${world ? "?world=1" : ""}`),
  // Pal advisor (hosted-model chat). GET says whether the process holds a
  // model API key at all and which provider it is; POST answers one
  // question. The context is the JSON summary lib/advisor.ts builds from
  // the same /pals payload the calculators render — the server adds the
  // prompt, never the data.
  advisorStatus: (id: number) => request<AdvisorStatus>(`/servers/${id}/advisor`),
  advisorChat: (id: number, context: string, messages: AdvisorMessage[], tools: AdvisorTool[] = []) =>
    request<AdvisorChatResponse>(`/servers/${id}/advisor`, {
      method: "POST",
      body: JSON.stringify({ context, messages, tools }),
    }),
  // Admin-only key management. The key is stored encrypted server-side and
  // takes effect immediately; both calls return the fresh status.
  setAdvisorKey: (provider: string, apiKey: string, model = "") =>
    request<AdvisorStatus>(`/advisor/key`, { method: "PUT", body: JSON.stringify({ provider, apiKey, model }) }),
  deleteAdvisorKey: () => request<AdvisorStatus>(`/advisor/key`, { method: "DELETE" }),
  // The signed-in user's own key — used in place of the shared one for
  // their requests only, stored encrypted against their account.
  setAdvisorSettings: (maxToolRounds: number) =>
    request<AdvisorStatus>(`/advisor/settings`, { method: "PUT", body: JSON.stringify({ maxToolRounds }) }),
  setMyAdvisorKey: (provider: string, apiKey: string, model = "") =>
    request<AdvisorStatus>(`/me/advisor-key`, { method: "PUT", body: JSON.stringify({ provider, apiKey, model }) }),
  deleteMyAdvisorKey: () => request<AdvisorStatus>(`/me/advisor-key`, { method: "DELETE" }),
  // Change which model a saved key runs, without re-entering the key.
  setAdvisorModel: (model: string) =>
    request<AdvisorStatus>(`/advisor/key/model`, { method: "PUT", body: JSON.stringify({ model }) }),
  setMyAdvisorModel: (model: string) =>
    request<AdvisorStatus>(`/me/advisor-key/model`, { method: "PUT", body: JSON.stringify({ model }) }),
  // Embedded project docs, for the advisor's docs-search tool. One blob,
  // cached by the tool for the session — it changes only with the binary.
  docs: () => request<{ docs: { name: string; content: string }[] }>(`/docs`),

  serverVisibility: (id: number) => request<VisibilityResult>(`/servers/${id}/visibility`),
  updateServerVisibility: (id: number, input: VisibilityInput) =>
    request<void>(`/servers/${id}/visibility`, { method: "PUT", body: JSON.stringify(input) }),

  // Activity: join/leave history for anyone signed in; the audit trail of
  // management actions is admin-only.
  serverActivity: (id: number, hours: number) => request<ActivityResult>(`/servers/${id}/activity?hours=${hours}`),
  serverAudit: (id: number, limit = 200) => request<{ entries: AuditEntry[] }>(`/servers/${id}/audit?limit=${limit}`),

  // Automation: restart schedules (readable by anyone signed in) and
  // Discord notifications (admin-only, and part of the same payload).
  serverAutomation: (id: number) => request<AutomationResult>(`/servers/${id}/automation`),
  createSchedule: (id: number, input: ScheduleWriteInput) =>
    request<RestartSchedule>(`/servers/${id}/schedules`, { method: "POST", body: JSON.stringify(input) }),
  updateSchedule: (id: number, scheduleId: number, input: ScheduleWriteInput) =>
    request<RestartSchedule>(`/servers/${id}/schedules/${scheduleId}`, { method: "PUT", body: JSON.stringify(input) }),
  deleteSchedule: (id: number, scheduleId: number) =>
    request<void>(`/servers/${id}/schedules/${scheduleId}`, { method: "DELETE" }),
  setDiscord: (id: number, input: DiscordWriteInput) =>
    request<DiscordConfig>(`/servers/${id}/discord`, { method: "PUT", body: JSON.stringify(input) }),
  deleteDiscord: (id: number) => request<void>(`/servers/${id}/discord`, { method: "DELETE" }),
  testDiscord: (id: number) => request<void>(`/servers/${id}/discord/test`, { method: "POST" }),
};
