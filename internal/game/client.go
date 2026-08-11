// Package game defines the contracts wildskeeper needs from a dedicated game
// server, with no knowledge of which game it is.
//
// The moderation, power, metrics-collection, scheduling and watchdog paths
// are written against these types alone: they never name a game, and a new
// one reaches them by implementing Client and registering a Definition (see
// registry.go) with nothing in that layer changing.
//
// The save-reading views are not there yet — internal/api still imports
// games/palworld's save and config readers directly, and cmd/wildskeeper wires
// the Palworld save reader concretely. docs/porting-to-another-game.md
// tracks what remains Palworld-shaped; this comment should not be read as
// claiming more than that document does.
//
// The interface below is deliberately the *intersection* of what the
// Source-derived dedicated-server population offers, because that intersection
// turns out to be remarkably stable: announce, kick, ban, unban, save, shut
// down, list players, report identity. A game that offers more exposes it
// through ExtendedClient rather than widening the contract everyone must meet.
package game

import "context"

// ServerInfo identifies a running server.
type ServerInfo struct {
	ServerName  string `json:"servername"`
	Version     string `json:"version"`
	PlayerCount int    `json:"playerCount"`
	// Transport reports which transport actually served this request (e.g.
	// "rest" or "rcon"). With a fallback client this can differ from the
	// server's configured preference, so the UI can say which one answered.
	Transport string `json:"transport"`
}

// Player is one connected player.
//
// PlayerUID and UserID are deliberately separate: most games have both an
// in-world character id and a platform account id, they are spelled
// differently, and the moderation commands want a specific one of the two.
type Player struct {
	Name string `json:"name"`
	// PlayerUID is the in-game/world id, whose spelling can vary by
	// transport — see Definition.CanonicalUID.
	PlayerUID string `json:"playerId"`
	// UserID is the platform (Steam/Xbox/…) id — the one kick/ban/unban
	// generally expect.
	UserID    string  `json:"userId"`
	Level     int     `json:"level"`
	Ping      float64 `json:"ping"`
	LocationX float64 `json:"location_x"`
	LocationY float64 `json:"location_y"`
}

// Metrics is the periodic health sample the collector charts. Fields a game
// cannot report stay zero rather than being modelled as optional — a flat
// line at zero reads correctly as "not reported" on a chart, and the
// alternative is a pointer per field for no gain.
type Metrics struct {
	ServerFPS        float64 `json:"serverfps"`
	ServerFrameTime  float64 `json:"serverframetime"`
	CurrentPlayerNum int     `json:"currentplayernum"`
	MaxPlayerNum     int     `json:"maxplayernum"`
	UptimeSeconds    int     `json:"uptime"`
	// Days is the in-game day counter, for games that keep one.
	Days int `json:"days"`
}

// Client is the set of operations wildskeeper needs from a game server,
// regardless of which transport carries them.
type Client interface {
	Info(ctx context.Context) (*ServerInfo, error)
	Players(ctx context.Context) ([]Player, error)
	Broadcast(ctx context.Context, message string) error
	Kick(ctx context.Context, playerUID, message string) error
	Ban(ctx context.Context, playerUID, message string) error
	Unban(ctx context.Context, playerUID string) error
	Save(ctx context.Context) error
	Shutdown(ctx context.Context, waitSeconds int, message string) error
}

// UnsupportedError reports that this game's client cannot perform an
// operation at all, as opposed to failing to reach a server that could.
// A game whose admin surface lacks a command (no RCON, no HTTP API, a
// command bridge that is down) returns this so the API can answer 501
// rather than 502 and the UI can say "this game can't" instead of
// "the server is unreachable".
type UnsupportedError struct {
	// Op is the Client method that cannot be served, lowercase — "broadcast",
	// "kick", "ban", "unban", "save", "shutdown".
	Op string
	// Reason is shown to the user verbatim, so it should say what is missing
	// and, when something can be done about it, what would light it up.
	Reason string
}

func (e *UnsupportedError) Error() string {
	if e.Reason == "" {
		return e.Op + " is not supported by this game"
	}
	return e.Op + " unsupported: " + e.Reason
}

// ExtendedClient is functionality only some transports can serve: a live
// settings dump and a metrics sample. RCON's command set has no equivalent of
// either in most games, so a plain RCON client will not implement this.
//
// Callers detect support with a type assertion — `ext, ok :=
// client.(ExtendedClient)` — and degrade rather than fail when it's absent.
type ExtendedClient interface {
	Settings(ctx context.Context) (map[string]any, error)
	Metrics(ctx context.Context) (*Metrics, error)
}

// CommandProber is implemented by clients that can say, without performing
// it, whether a command would be served right now. It exists so the UI can
// state the truth up front — "this server saves before a restart" — instead
// of hedging or discovering the answer by firing the command and reading the
// 501 that comes back.
//
// Only a client whose support genuinely varies per server needs this.
// Dragonwilds does: an on-demand save exists only when the dwbridge mod is
// running, which is a property of the machine, not of the game. A client
// with a fixed command set simply doesn't implement the interface, and
// callers fall back to assuming a command is available — the same optimism
// they had before probing existed.
//
// Supports reports whether op (the lowercase Client method name) can be
// served, and when it can't, the same reason an *UnsupportedError would
// carry. It must have no side effects and must agree with what actually
// running op would do, so implementations should share one decision between
// the two rather than restating it.
type CommandProber interface {
	Supports(ctx context.Context, op string) (bool, string)
}

// Conn carries connection details for one server. Passwords are expected to
// already be decrypted by the caller.
type Conn struct {
	Host         string
	RESTPort     int
	RESTPassword string
	RCONPort     int
	RCONPassword string
	// PreferREST asks for the game's HTTP admin API where it has one, with
	// RCON as the fallback. Games with no HTTP API ignore it.
	PreferREST bool
	// AgentURL and AgentToken reach the server's wkagent sidecar. Games with
	// no query protocol of their own (Dragonwilds) derive their state through
	// the agent — process liveness from its health, players from its log tail
	// — so for them the agent is the admin transport, not an extra.
	AgentURL   string
	AgentToken string
}
