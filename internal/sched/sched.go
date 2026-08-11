// Package sched runs scheduled server restarts: in-game warning broadcasts
// at configured lead times, then save → in-game shutdown → container
// restart at the scheduled minute.
//
// The sweep is stateless against the database — every tick re-reads the
// schedules, so edits in the UI take effect by the next tick with no
// signaling. Firing is deduplicated in memory per (schedule, occurrence);
// on process restart the dedupe map is empty, but the stale window below
// keeps a rebooted Wildskeeper from replaying events that are more than a
// couple of minutes old.
package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/safwyls/wildskeeper/internal/agentctl"
	"github.com/safwyls/wildskeeper/internal/dockerctl"
	"github.com/safwyls/wildskeeper/internal/game"
	"github.com/safwyls/wildskeeper/internal/notify"
	"github.com/safwyls/wildskeeper/internal/store"
)

const (
	// tick is how often due events are checked for. Warnings land within
	// tick of their nominal minute, which is plenty for a countdown.
	tick = 20 * time.Second
	// staleWindow is how far past an event's time it may still fire, so a
	// short stall (or one missed tick) doesn't drop a restart, but a
	// laptop waking from an hour's sleep doesn't restart anything.
	staleWindow = 2 * time.Minute
	// suppressFor mutes "server unreachable" Discord alerts around a
	// planned restart. If the server is still down after this, the alert
	// fires — at that point it's a real problem.
	suppressFor = 10 * time.Minute
)

type Scheduler struct {
	store    *store.Store
	notifier *notify.Notifier
	// docker is nil when power control isn't configured; restarts then
	// fall back to an in-game shutdown and rely on the container's
	// restart policy to bring the server back.
	docker *dockerctl.Client
	logger *slog.Logger

	// fired dedupes events within a process lifetime, keyed by
	// scheduleID + occurrence time.
	fired map[string]time.Time
}

func New(st *store.Store, notifier *notify.Notifier, docker *dockerctl.Client, logger *slog.Logger) *Scheduler {
	return &Scheduler{store: st, notifier: notifier, docker: docker, logger: logger, fired: make(map[string]time.Time)}
}

// Run sweeps until ctx is cancelled. Intended to be started in a goroutine.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx, time.Now())
		}
	}
}

// event is one due action: a warning broadcast (Minutes > 0) or the
// restart itself (Minutes == 0).
type event struct {
	At      time.Time
	Minutes int
}

// dueEvents returns the schedule's events (warnings and restart) whose time
// falls in (now-staleWindow, now]. Warning times are derived from the
// restart's occurrence, so a warning for a 00:05 restart correctly lands
// the evening before even when that evening's weekday isn't scheduled.
func dueEvents(sc *store.RestartSchedule, now time.Time) []event {
	hour, minute, ok := parseTimeOfDay(sc.TimeOfDay)
	if !ok {
		return nil
	}
	var out []event
	// A warning can precede its restart by up to ~3h; scanning the restart
	// occurrences of yesterday, today and tomorrow covers every event that
	// could possibly be due now.
	for dayOffset := -1; dayOffset <= 1; dayOffset++ {
		day := now.AddDate(0, 0, dayOffset)
		if !containsInt(sc.Days, int(day.Weekday())) {
			continue
		}
		restartAt := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, now.Location())
		for _, m := range sc.WarningMinutes {
			out = append(out, event{At: restartAt.Add(-time.Duration(m) * time.Minute), Minutes: m})
		}
		out = append(out, event{At: restartAt})
	}
	due := out[:0]
	for _, e := range out {
		if e.At.After(now.Add(-staleWindow)) && !e.At.After(now) {
			due = append(due, e)
		}
	}
	return due
}

// NextRun returns the next restart occurrence strictly after `after`, or
// the zero time when the schedule can never fire (no days, bad time). The
// API uses it to show "next restart" in the UI.
func NextRun(sc *store.RestartSchedule, after time.Time) time.Time {
	hour, minute, ok := parseTimeOfDay(sc.TimeOfDay)
	if !ok || len(sc.Days) == 0 {
		return time.Time{}
	}
	for dayOffset := 0; dayOffset <= 7; dayOffset++ {
		day := after.AddDate(0, 0, dayOffset)
		if !containsInt(sc.Days, int(day.Weekday())) {
			continue
		}
		t := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, after.Location())
		if t.After(after) {
			return t
		}
	}
	return time.Time{}
}

func (s *Scheduler) sweep(ctx context.Context, now time.Time) {
	schedules, err := s.store.ListAllRestartSchedules(ctx)
	if err != nil {
		s.logger.Error("scheduler: listing schedules", "error", err)
		return
	}
	if len(schedules) == 0 {
		return
	}

	// One server lookup per sweep even if it has several schedules.
	servers := map[int64]*store.Server{}
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		srv, ok := servers[sc.ServerID]
		if !ok {
			srv, err = s.store.GetServer(ctx, sc.ServerID)
			if err != nil {
				s.logger.Error("scheduler: loading server", "server", sc.ServerID, "error", err)
				continue
			}
			servers[sc.ServerID] = srv
		}
		if !srv.Enabled {
			continue
		}
		for _, e := range dueEvents(sc, now) {
			key := fmt.Sprintf("%d@%d", sc.ID, e.At.Unix())
			if _, done := s.fired[key]; done {
				continue
			}
			s.fired[key] = now
			if e.Minutes > 0 {
				s.warn(ctx, srv, e.Minutes)
			} else {
				// Restarts run in their own goroutine so the save/shutdown
				// sequence (up to ~40s) doesn't stall warnings for other
				// servers in the same sweep.
				go s.restart(context.WithoutCancel(ctx), srv, sc)
			}
		}
	}

	for key, at := range s.fired {
		if now.Sub(at) > 24*time.Hour {
			delete(s.fired, key)
		}
	}
}

func (s *Scheduler) warn(ctx context.Context, srv *store.Server, minutes int) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	unit := "minutes"
	if minutes == 1 {
		unit = "minute"
	}
	msg := fmt.Sprintf("Server restart in %d %s", minutes, unit)

	// Discord first, and unconditionally: it needs no game client, and the
	// restart it is warning about goes ahead even for a row naming a game
	// this build doesn't register. Warning in-game and warning subscribers
	// are separate promises, so one failing must not cancel the other.
	s.notifier.RestartWarning(ctx, srv, minutes)

	client, err := srv.Client()
	if err != nil {
		s.logger.Info("scheduler: cannot warn in game", "server", srv.ID, "error", err)
		return
	}
	if err := client.Broadcast(ctx, msg); err != nil {
		// An unreachable server can't be warned — and doesn't need to be.
		s.logger.Info("scheduler: warning broadcast failed", "server", srv.ID, "minutes", minutes, "error", err)
	} else {
		s.logger.Info("scheduler: restart warning broadcast", "server", srv.ID, "minutes", minutes)
	}
}

// saveBeforeRestart writes the world before the process goes down, and
// classifies what happened. The restart proceeds either way — a server too
// wedged to save is the one most in need of restarting — but the three
// outcomes mean very different things to an operator, so they must not
// collapse into one "failed" line.
//
// For Dragonwilds this is the whole point of the sequence: the game does
// not save on shutdown and autosaves only every ~5 minutes, so the save is
// the only thing standing between a scheduled restart and lost play. It
// reaches the game through the dwbridge mod; without one, Save answers
// *game.UnsupportedError, which is a standing capability gap rather than a
// blip and is worth naming as such every time it costs someone their
// progress.
func (s *Scheduler) saveBeforeRestart(ctx context.Context, srv *store.Server, client game.Client) notify.SaveOutcome {
	err := client.Save(ctx)
	if err == nil {
		s.logger.Info("scheduler: world saved before restart", "server", srv.ID)
		return notify.SaveDone
	}
	var unsupported *game.UnsupportedError
	if errors.As(err, &unsupported) {
		s.logger.Warn("scheduler: no way to save this server before restarting; anything since the last autosave will be lost",
			"server", srv.ID, "reason", unsupported.Reason)
		return notify.SaveUnsupported
	}
	s.logger.Warn("scheduler: save before restart failed; restarting anyway", "server", srv.ID, "error", err)
	return notify.SaveFailed
}

// restart mirrors the manual power flow (save → in-game shutdown → agent
// or container restart), with each step best-effort: a hung server that
// can't save is exactly the server most in need of the restart that
// follows.
func (s *Scheduler) restart(ctx context.Context, srv *store.Server, sc *store.RestartSchedule) {
	// Wide enough for the worst legitimate case, which the steps' own
	// timeouts add up to: save (25s) + in-game shutdown (25s) + the restart
	// itself — up to 110s through an agent that waits out the self-exit
	// window, or 90s through docker's stop grace. A tighter budget cuts the
	// last step off partway, which is the one step that must not be
	// interrupted. Restarts already run in their own goroutine, so a slow
	// one delays nothing else.
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	s.notifier.SuppressStatus(srv.ID, suppressFor)
	s.logger.Info("scheduler: restarting server", "server", srv.ID, "name", srv.Name, "schedule", sc.ID)

	// A client failure here is a row naming an unknown game, not an
	// unreachable server: skip the in-game steps and let the container
	// restart below still happen.
	client, clientErr := srv.Client()
	saveOutcome := notify.SaveSkipped
	if clientErr != nil {
		s.logger.Warn("scheduler: no game client; restarting container only", "server", srv.ID, "error", clientErr)
	} else {
		stepCtx, stepCancel := context.WithTimeout(ctx, 25*time.Second)
		saveOutcome = s.saveBeforeRestart(stepCtx, srv, client)
		stepCancel()
	}

	// Announced after the save rather than before it, so the notice can
	// report what actually happened. The cost is up to the save's own 25s
	// budget of delay on a message players already had lead-time warnings
	// for; the benefit is that it never claims a save that didn't happen.
	s.notifier.RestartingNow(ctx, srv, saveOutcome)

	// A supervisor-mode agent owns the game process, so it does the
	// restarting — checked before docker for the same reason the manual
	// power flow checks it first. It matters more here since provisioning
	// began recording container names: a provisioned server now has both an
	// agent and a container name, and only the agent is guaranteed to be
	// looking at the same machine wildskeeper's docker proxy is.
	agent, _ := agentctl.Supervisor(ctx, srv.AgentURL, srv.AgentToken)
	useDocker := agent == nil && s.docker != nil && srv.ContainerName != ""

	// When something is standing by to restart the server the moment the
	// game exits, the game only needs to exit *cleanly* (code 0 rather than
	// SIGKILL's 137) and a countdown would be dead air. With neither, the
	// in-game shutdown IS the restart — the container's restart policy
	// revives the process — so players get a real countdown.
	wait := 10
	if agent != nil || useDocker {
		wait = 1
	}
	shutdownOK := false
	if clientErr == nil {
		stepCtx, stepCancel := context.WithTimeout(ctx, 25*time.Second)
		if err := client.Shutdown(stepCtx, wait, "Server restarting"); err != nil {
			s.logger.Warn("scheduler: in-game shutdown failed", "server", srv.ID, "error", err)
		} else {
			shutdownOK = true
		}
		stepCancel()
	}

	switch {
	case agent != nil:
		// The agent signals the game's whole process group, so a signal
		// landing on top of an accepted in-game shutdown catches the engine
		// mid-save and ends it at 143 instead of 0. Let that exit finish
		// first — but only when the shutdown was actually accepted.
		graceful := time.Duration(0)
		if shutdownOK {
			graceful = agentctl.GameSelfExitWindow
		}
		if _, err := agent.Power(ctx, "restart", graceful); err != nil {
			s.logger.Error("scheduler: agent restart failed", "server", srv.ID, "error", err)
			return
		}
	case useDocker:
		if err := s.docker.Restart(ctx, srv.ContainerName); err != nil {
			s.logger.Error("scheduler: container restart failed", "server", srv.ID, "container", srv.ContainerName, "error", err)
			return
		}
	}

	if err := s.store.MarkRestartScheduleRun(ctx, sc.ID, time.Now()); err != nil {
		s.logger.Error("scheduler: recording run", "schedule", sc.ID, "error", err)
	}
	// Scheduled restarts join the same audit trail as manual power actions,
	// so the "who restarted the server at 5am" question has one answer page —
	// and, since the answer to "did we lose anything?" is only knowable at
	// the moment of the restart, the save outcome is recorded with it.
	detail := fmt.Sprintf("%s · %s", sc.TimeOfDay, saveOutcome)
	if err := s.store.InsertAudit(ctx, srv.ID, "scheduler", "scheduled-restart", detail); err != nil {
		s.logger.Error("scheduler: recording audit entry", "schedule", sc.ID, "error", err)
	}
	s.logger.Info("scheduler: restart complete", "server", srv.ID, "schedule", sc.ID, "docker", useDocker, "save", saveOutcome.String())
}

func parseTimeOfDay(s string) (hour, minute int, ok bool) {
	if _, err := fmt.Sscanf(s, "%d:%d", &hour, &minute); err != nil {
		return 0, 0, false
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

func containsInt(vals []int, v int) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}
