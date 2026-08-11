package sched

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/safwyls/wildskeeper/internal/crypto"
	"github.com/safwyls/wildskeeper/internal/db"
	"github.com/safwyls/wildskeeper/internal/game/gametest"
	"github.com/safwyls/wildskeeper/internal/notify"
	"github.com/safwyls/wildskeeper/internal/store"

	// The scheduler talks to a server through its game client, which only
	// resolves once the registry is populated the way the binary does it.
	_ "github.com/safwyls/wildskeeper/internal/games"
)

// gameSpy is a Palworld REST endpoint that records the calls the scheduler
// makes on its way through save → shutdown.
type gameSpy struct {
	mu    sync.Mutex
	calls []string
	// bodies keeps the last decoded payload per path, so a test can assert
	// what was actually asked for and not just that something was.
	bodies map[string]map[string]any
	// statuses overrides the response code for a path, so a test can make a
	// single step fail — 501 for "this game can't", anything else for a
	// fault — while the rest of the sequence behaves.
	statuses map[string]int
}

func newGameSpy(t *testing.T) (*gameSpy, string) {
	t.Helper()
	spy := &gameSpy{bodies: map[string]map[string]any{}, statuses: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spy.mu.Lock()
		spy.calls = append(spy.calls, r.URL.Path)
		if r.Body != nil {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) == nil {
				spy.bodies[r.URL.Path] = body
			}
		}
		status := spy.statuses[r.URL.Path]
		spy.mu.Unlock()
		if status != 0 {
			http.Error(w, "the fake game refused "+r.URL.Path, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/api/info":
			json.NewEncoder(w).Encode(map[string]any{"servername": "s"})
		case "/v1/api/players":
			json.NewEncoder(w).Encode(map[string]any{"players": []any{}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return spy, srv.URL
}

func (g *gameSpy) saw(path string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Contains(strings.Join(g.calls, " "), path)
}

// answer makes path respond with the given status instead of succeeding.
func (g *gameSpy) answer(path string, status int) {
	g.mu.Lock()
	g.statuses[path] = status
	g.mu.Unlock()
}

func (g *gameSpy) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *gameSpy) body(path string) map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bodies[path]
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	box, err := crypto.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return store.New(sqlDB, box)
}

func newScheduler(t *testing.T, st *store.Store) *Scheduler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, notify.New(st, logger), nil, logger)
}

// addServer registers a REST-mode row aimed at the spy.
func addServer(t *testing.T, st *store.Store, rawURL string, enabled bool) *store.Server {
	t.Helper()
	u, _ := url.Parse(rawURL)
	port, _ := strconv.Atoi(u.Port())
	srv := &store.Server{
		Name: "main", Host: u.Hostname(), Game: gametest.ID,
		RESTPort: port, RESTPassword: "pw",
		RCONPort: 25575, RCONPassword: "pw",
		UseREST: true, Enabled: enabled,
	}
	id, err := st.CreateServer(context.Background(), srv)
	if err != nil {
		t.Fatal(err)
	}
	srv.ID = id
	return srv
}

// addSchedule stores a schedule that is due at `at`.
func addSchedule(t *testing.T, st *store.Store, serverID int64, at time.Time, warnings []int, enabled bool) *store.RestartSchedule {
	t.Helper()
	sc := &store.RestartSchedule{
		ServerID:       serverID,
		Enabled:        enabled,
		Days:           []int{int(at.Weekday())},
		TimeOfDay:      at.Format("15:04"),
		WarningMinutes: warnings,
	}
	id, err := st.CreateRestartSchedule(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	sc.ID = id
	return sc
}

func TestSweepBroadcastsAWarning(t *testing.T) {
	st := newStore(t)
	spy, addr := newGameSpy(t)
	srv := addServer(t, st, addr, true)

	now := time.Now().Truncate(time.Minute)
	// Restart in 5 minutes, with a 5-minute warning — so the warning is due
	// right now.
	addSchedule(t, st, srv.ID, now.Add(5*time.Minute), []int{5}, true)

	newScheduler(t, st).sweep(context.Background(), now)

	if !spy.saw("/v1/api/announce") {
		t.Errorf("no warning broadcast: %v", spy.calls)
	}
}

func TestSweepRunsTheRestartSequence(t *testing.T) {
	st := newStore(t)
	spy, addr := newGameSpy(t)
	srv := addServer(t, st, addr, true)
	sc := addSchedule(t, st, srv.ID, time.Now().Truncate(time.Minute), nil, true)

	s := newScheduler(t, st)
	s.sweep(context.Background(), time.Now().Truncate(time.Minute))

	// The restart runs in its own goroutine so warnings for other servers
	// aren't stalled behind it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !spy.saw("/v1/api/shutdown") {
		time.Sleep(10 * time.Millisecond)
	}

	if !spy.saw("/v1/api/save") {
		t.Errorf("the world was never saved before the restart: %v", spy.calls)
	}
	if !spy.saw("/v1/api/shutdown") {
		t.Errorf("no in-game shutdown: %v", spy.calls)
	}

	// The run is recorded so the next sweep knows it happened.
	got, err := st.GetRestartSchedule(context.Background(), sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastRunAt == nil || got.LastRunAt.IsZero() {
		t.Errorf("the run was not recorded: %+v", got)
	}
}

// Every event fires once. Without the dedupe a schedule due "this minute"
// would restart the server on every 20-second tick.
func TestSweepFiresEachEventOnce(t *testing.T) {
	st := newStore(t)
	spy, addr := newGameSpy(t)
	srv := addServer(t, st, addr, true)
	now := time.Now().Truncate(time.Minute)
	addSchedule(t, st, srv.ID, now.Add(5*time.Minute), []int{5}, true)

	s := newScheduler(t, st)
	s.sweep(context.Background(), now)
	first := spy.count()
	s.sweep(context.Background(), now.Add(10*time.Second))
	s.sweep(context.Background(), now.Add(20*time.Second))

	if spy.count() != first {
		t.Errorf("the warning fired again on later ticks: %d then %d calls", first, spy.count())
	}
}

func TestSweepSkipsDisabledScheduleOrServer(t *testing.T) {
	t.Run("disabled schedule", func(t *testing.T) {
		st := newStore(t)
		spy, addr := newGameSpy(t)
		srv := addServer(t, st, addr, true)
		now := time.Now().Truncate(time.Minute)
		addSchedule(t, st, srv.ID, now.Add(5*time.Minute), []int{5}, false)

		newScheduler(t, st).sweep(context.Background(), now)
		if spy.count() != 0 {
			t.Errorf("a disabled schedule fired: %v", spy.calls)
		}
	})

	t.Run("disabled server", func(t *testing.T) {
		st := newStore(t)
		spy, addr := newGameSpy(t)
		srv := addServer(t, st, addr, false)
		now := time.Now().Truncate(time.Minute)
		addSchedule(t, st, srv.ID, now.Add(5*time.Minute), []int{5}, true)

		newScheduler(t, st).sweep(context.Background(), now)
		if spy.count() != 0 {
			t.Errorf("a schedule on a disabled server fired: %v", spy.calls)
		}
	})
}

func TestSweepWithNothingScheduled(t *testing.T) {
	st := newStore(t)
	// No schedules at all is the common case and must not error or panic.
	newScheduler(t, st).sweep(context.Background(), time.Now())
}

// A schedule whose server row has gone is skipped, not fatal — the sweep
// still has other servers to serve.
func TestSweepSurvivesAMissingServer(t *testing.T) {
	st := newStore(t)
	spy, addr := newGameSpy(t)
	srv := addServer(t, st, addr, true)
	now := time.Now().Truncate(time.Minute)
	addSchedule(t, st, srv.ID, now.Add(5*time.Minute), []int{5}, true)

	if err := st.DeleteServer(context.Background(), srv.ID); err != nil {
		t.Fatal(err)
	}
	newScheduler(t, st).sweep(context.Background(), now)
	if spy.count() != 0 {
		t.Errorf("a schedule for a deleted server still fired: %v", spy.calls)
	}
}

// The dedupe map must not grow without bound across a long uptime.
func TestFiredEntriesAreEventuallyForgotten(t *testing.T) {
	st := newStore(t)
	_, addr := newGameSpy(t)
	srv := addServer(t, st, addr, true)
	now := time.Now().Truncate(time.Minute)
	addSchedule(t, st, srv.ID, now.Add(5*time.Minute), []int{5}, true)

	s := newScheduler(t, st)
	s.sweep(context.Background(), now)
	if len(s.fired) == 0 {
		t.Fatal("nothing was recorded as fired")
	}
	s.sweep(context.Background(), now.Add(25*time.Hour))
	if len(s.fired) != 0 {
		t.Errorf("stale dedupe entries survived: %v", s.fired)
	}
}

// A warning for an unreachable server is logged, not fatal — and the Discord
// notice still goes out, since that's the half that still works.
func TestWarnSurvivesAnUnreachableServer(t *testing.T) {
	st := newStore(t)
	srv := addServer(t, st, "http://127.0.0.1:1", true)
	newScheduler(t, st).warn(context.Background(), srv, 5)
}

func TestRunStopsOnContextCancel(t *testing.T) {
	st := newStore(t)
	s := newScheduler(t, st)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
