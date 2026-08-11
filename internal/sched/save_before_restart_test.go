package sched

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/safwyls/wildskeeper/internal/store"
)

// lastRestartAudit returns the detail recorded for the scheduled restart.
func lastRestartAudit(t *testing.T, st *store.Store, serverID int64) string {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), serverID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "scheduled-restart" {
			return e.Detail
		}
	}
	t.Fatalf("no scheduled-restart audit entry among %+v", entries)
	return ""
}

// The happy path: the world reached disk, and the record says so.
func TestScheduledRestartRecordsThatTheWorldWasSaved(t *testing.T) {
	st := newStore(t)
	game, gameURL := newGameSpy(t)
	srv := addServer(t, st, gameURL, true)
	sc := addSchedule(t, st, srv.ID, time.Now().Truncate(time.Minute), nil, true)

	newScheduler(t, st).restart(context.Background(), srv, sc)

	if !game.saw("/v1/api/save") {
		t.Fatalf("no save was attempted before the restart: %v", game.calls)
	}
	if detail := lastRestartAudit(t, st, srv.ID); !strings.Contains(detail, "world saved") {
		t.Errorf("audit detail = %q, want it to record the save", detail)
	}
}

// The case this whole sequence exists for. Dragonwilds does not save on
// shutdown, so a server with no dwbridge mod loses everything since the last
// autosave on every scheduled restart. That is a standing capability gap,
// not a blip, and it must be recorded as its own thing rather than as a
// generic failure — otherwise the one signal that would tell an operator to
// go install the mod reads like a transient error.
func TestScheduledRestartRecordsWhenThereIsNoWayToSave(t *testing.T) {
	st := newStore(t)
	game, gameURL := newGameSpy(t)
	game.answer("/v1/api/save", http.StatusNotImplemented)
	srv := addServer(t, st, gameURL, true)
	sc := addSchedule(t, st, srv.ID, time.Now().Truncate(time.Minute), nil, true)

	newScheduler(t, st).restart(context.Background(), srv, sc)

	detail := lastRestartAudit(t, st, srv.ID)
	if !strings.Contains(detail, "no command bridge") {
		t.Errorf("audit detail = %q, want it to name the missing bridge", detail)
	}
	if strings.Contains(detail, "world saved") {
		t.Errorf("audit detail = %q claims a save that could not happen", detail)
	}
	// A server that cannot save is still restarted — that was never
	// conditional on the save working.
	if !game.saw("/v1/api/shutdown") {
		t.Errorf("the restart was abandoned because the save was unsupported: %v", game.calls)
	}
}

// A save that was possible and went wrong is a different signal from one
// that was never possible: usually transient, and not a reason to go
// install anything.
func TestScheduledRestartDistinguishesAFailedSaveFromAnImpossibleOne(t *testing.T) {
	st := newStore(t)
	game, gameURL := newGameSpy(t)
	game.answer("/v1/api/save", http.StatusInternalServerError)
	srv := addServer(t, st, gameURL, true)
	sc := addSchedule(t, st, srv.ID, time.Now().Truncate(time.Minute), nil, true)

	newScheduler(t, st).restart(context.Background(), srv, sc)

	detail := lastRestartAudit(t, st, srv.ID)
	if !strings.Contains(detail, "save failed") {
		t.Errorf("audit detail = %q, want it to record the failure", detail)
	}
	if strings.Contains(detail, "no command bridge") {
		t.Errorf("audit detail = %q blames a missing bridge for a fault", detail)
	}
	if !game.saw("/v1/api/shutdown") {
		t.Errorf("the restart was abandoned because the save failed: %v", game.calls)
	}
}
