import { useEffect, useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CalendarClock,
  Copy,
  Dog,
  Download,
  Globe,
  HardDriveDownload,
  Pencil,
  Plus,
  RotateCw,
  Send,
  Trash2,
  Webhook,
} from "lucide-react";
import { toast } from "sonner";
import {
  api,
  errorDetail,
  type AutomationResult,
  type DiscordConfig,
  type RestartSchedule,
  type ScheduleWriteInput,
} from "../lib/api";
import { useAuth } from "../lib/auth";
import { useCommand } from "../lib/capabilities";
import { agoLabel } from "../lib/time";
import { cn } from "../lib/utils";
import { Button } from "../components/ui/button";
import { TIER_LOOKS, TierTile, rampLook, tierText, type TierLook } from "../components/TierTile";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Select } from "../components/ui/select";
import { Switch } from "../components/ui/switch";

const DAY_LETTERS = ["S", "M", "T", "W", "T", "F", "S"];
const WARNING_PRESETS = [60, 30, 15, 10, 5, 1];

/** "in 2d 4h" / "in 9h 12m" / "in 3m" — coarse on purpose; the exact
 * minute is already printed next to it. */
function countdown(to: Date, from: Date): string {
  const mins = Math.round((to.getTime() - from.getTime()) / 60_000);
  if (mins <= 0) return "now";
  const d = Math.floor(mins / 1440);
  const h = Math.floor((mins % 1440) / 60);
  const m = mins % 60;
  if (d > 0) return `in ${d}d ${h}h`;
  if (h > 0) return `in ${h}h ${m}m`;
  return `in ${m}m`;
}

export function ServerAutomation() {
  const { serverID } = useParams();
  const id = Number(serverID);
  const { isAdmin } = useAuth();
  const [editing, setEditing] = useState<RestartSchedule | "new" | null>(null);

  const automationQuery = useQuery({
    queryKey: ["automation", id],
    queryFn: () => api.serverAutomation(id),
    refetchInterval: 60_000,
  });

  const data = automationQuery.data;

  return (
    <div className="pb-24">
      <header className="sticky top-0 z-10 hidden items-center justify-between border-b border-wk-edge bg-wk-bg px-8 py-6 lg:flex">
        <div>
          <h1 className="font-display text-2xl font-extrabold">Automation</h1>
          <p className="text-sm text-wk-parchment/60">Scheduled restarts and Discord notifications</p>
        </div>
      </header>

      <div className="mx-auto max-w-5xl space-y-4 p-4 lg:space-y-6 lg:p-8">
        {automationQuery.isError && (
          <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-wk-parchment/70">
            Automation settings could not be loaded. Refresh to try again.
          </p>
        )}

        {data && <NextRestartHero schedules={data.schedules} />}

        {data && (
          // grid-cols-1 is load-bearing on mobile: its minmax(0,1fr) stops
          // one card's min-content (a long URL, a mono timestamp row) from
          // widening the whole column past the viewport.
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-5 lg:gap-6">
            <SchedulesCard
              serverId={id}
              data={data}
              canEdit={isAdmin}
              onAdd={() => setEditing("new")}
              onEdit={(sc) => setEditing(sc)}
            />
            {data.discord && <DiscordCard serverId={id} config={data.discord} />}
            {data.watchdog && <WatchdogCard serverId={id} config={data.watchdog} />}
            {data.publicStatus && <PublicStatusCard serverId={id} config={data.publicStatus} />}
            {isAdmin && <BackupsCard serverId={id} />}
          </div>
        )}
      </div>

      <ScheduleDialog
        serverId={id}
        schedule={editing === "new" ? null : editing}
        open={editing !== null}
        onOpenChange={(open) => !open && setEditing(null)}
      />
    </div>
  );
}

/**
 * How far out the restart is, as a tier tile. The ramp reads the game's
 * passive tiers as a countdown — Rainbow aqua while it's still tomorrow's
 * problem, then gold, then the tier-1 ice grey, and finally the negative
 * red once it's minutes away. Stops are interpolated on a log scale of
 * minutes, so the tile warms imperceptibly from one half-minute tick to the
 * next but visibly over the course of an afternoon.
 */
// Anchors are placed so a daily restart spends its daytime on Rainbow aqua
// and only warms through the evening: pure red owns the last ten minutes,
// which is when the in-game warnings actually fire.
const RESTART_RAMP: [number, TierLook][] = [
  [Math.log(5), TIER_LOOKS.red], //      5 minutes
  [Math.log(45), TIER_LOOKS.ice], //    45 minutes
  [Math.log(180), TIER_LOOKS.gold], //   3 hours
  [Math.log(720), TIER_LOOKS.aqua], //  12 hours
];

function restartLook(minutes: number): TierLook {
  return rampLook(RESTART_RAMP, Math.log(Math.max(1, minutes)));
}

/** The page's one bold element: the next restart, with the warning
 * broadcasts drawn as a cadence rail running into the restart moment. */
function NextRestartHero({ schedules }: { schedules: RestartSchedule[] }) {
  // Re-render each half-minute so the countdown stays honest.
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 30_000);
    return () => clearInterval(t);
  }, []);

  const next = useMemo(() => {
    let best: { at: Date; schedule: RestartSchedule } | null = null;
    for (const sc of schedules) {
      if (!sc.enabled || !sc.nextRunAt) continue;
      const at = new Date(sc.nextRunAt);
      if (!best || at < best.at) best = { at, schedule: sc };
    }
    return best;
  }, [schedules]);

  if (!next) return null;

  const when = next.at.toLocaleString([], { weekday: "long", hour: "2-digit", minute: "2-digit" });
  // MOCKUP ONLY: ?restartIn=<minutes> previews any point on the ramp, like
  // the Paldex hero's ?heroPct=.
  const preview = new URLSearchParams(window.location.search).get("restartIn");
  const minutesOut = preview !== null ? Number(preview) : (next.at.getTime() - now.getTime()) / 60_000;
  const look = restartLook(minutesOut);

  return (
    <TierTile
      look={look}
      eyebrow="Next restart"
      value={when}
      // A weekday-and-clock string is a lot longer than the Paldex tile's
      // two-digit percentage, so it sits one step down the scale.
      valueClass="text-3xl lg:text-4xl"
      sub={countdown(next.at, now)}
      footer={
        next.schedule.warningMinutes.length > 0 && (
          <div
            // Six warning presets don't fit a phone; the rail scrolls rather
            // than wrapping, which would break the run-up-to-restart reading.
            className="no-scrollbar mt-4 flex items-center gap-2 overflow-x-auto"
            aria-label="Players are warned in-game before the restart"
          >
            {next.schedule.warningMinutes.map((m) => (
              <div key={m} className="flex shrink-0 items-center gap-2">
                <div className="flex flex-col items-center">
                  <span className="font-mono text-[11px] leading-tight" style={{ color: tierText(look, 0.7) }}>
                    −{m}m
                  </span>
                  <span className="mt-1 h-1.5 w-1.5 rounded-full" style={{ backgroundColor: look.accent }} />
                </div>
                <span className="h-px w-6 lg:w-12" style={{ backgroundColor: tierText(look, 0.25) }} />
              </div>
            ))}
            <span
              className="clip-notch flex shrink-0 items-center gap-1.5 px-2.5 py-1 font-display text-xs font-bold"
              style={{ backgroundColor: look.accent, color: look.ground[1] }}
            >
              <RotateCw className="h-3 w-3" /> restart
            </span>
          </div>
        )
      }
    />
  );
}

function DayDots({ days, size = "sm" }: { days: number[]; size?: "sm" | "lg" }) {
  return (
    <span className="flex gap-1" aria-hidden>
      {DAY_LETTERS.map((letter, i) => (
        <span
          key={i}
          className={cn(
            "flex items-center justify-center rounded-full font-display font-bold",
            size === "sm" ? "h-5 w-5 text-[10px]" : "h-8 w-8 text-xs",
            days.includes(i) ? "bg-wk-ink text-wk-parchment" : "text-wk-parchment/25",
          )}
        >
          {letter}
        </span>
      ))}
    </span>
  );
}

function describeDays(days: number[]): string {
  if (days.length === 7) return "Every day";
  const names = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
  return days.map((d) => names[d]).join(", ");
}

function SchedulesCard({
  serverId,
  data,
  canEdit,
  onAdd,
  onEdit,
}: {
  serverId: number;
  data: AutomationResult;
  canEdit: boolean;
  onAdd: () => void;
  onEdit: (sc: RestartSchedule) => void;
}) {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["automation", serverId] });
  // A scheduled restart saves first — but only where something can carry
  // the save. Asking lets this card describe what will happen to *this*
  // server instead of listing both possibilities.
  const saveCmd = useCommand(serverId, "save");

  const toggle = useMutation({
    mutationFn: (sc: RestartSchedule) =>
      api.updateSchedule(serverId, sc.id, {
        enabled: !sc.enabled,
        days: sc.days,
        timeOfDay: sc.timeOfDay,
        warningMinutes: sc.warningMinutes,
      }),
    onSuccess: (updated) => {
      toast.success(updated.enabled ? "Schedule on" : "Schedule off");
      invalidate();
    },
    onError: (err) => toast.error("Could not update schedule", { description: errorDetail(err) }),
  });

  const remove = useMutation({
    mutationFn: (scheduleId: number) => api.deleteSchedule(serverId, scheduleId),
    onSuccess: () => {
      toast.success("Schedule removed");
      invalidate();
    },
    onError: (err) => toast.error("Could not remove schedule", { description: errorDetail(err) }),
  });

  return (
    <section className="rounded-xl border border-wk-edge bg-wk-panel lg:col-span-3">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-wk-edge px-5 py-4">
        <div className="flex items-center gap-2">
          <CalendarClock className="h-4 w-4 text-wk-brasshi" />
          <h2 className="font-display text-base font-bold">Scheduled restarts</h2>
        </div>
        {canEdit && (
          <Button size="sm" onClick={onAdd}>
            <Plus className="h-4 w-4" /> Add schedule
          </Button>
        )}
      </div>

      {data.schedules.length === 0 ? (
        <div className="px-5 py-8 text-center">
          <p className="text-sm text-wk-parchment/60">No restart schedules yet.</p>
          {canEdit && (
            <p className="mt-1 text-sm text-wk-parchment/60">
              Add one and Wildskeeper restarts the server on schedule.{" "}
              {saveCmd.known && !saveCmd.supported
                ? "This server has no way to save on demand, so a restart loses anything since the game's last autosave."
                : "The world is saved first, so a restart costs nothing."}
            </p>
          )}
        </div>
      ) : (
        <ul className="divide-y divide-wk-edge">
          {data.schedules.map((sc) => (
            <li
              key={sc.id}
              className={cn("flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3.5", !sc.enabled && "opacity-50")}
            >
              <span className="font-mono text-lg font-medium tabular-nums">{sc.timeOfDay}</span>
              <div className="min-w-0">
                <DayDots days={sc.days} />
                <p className="mt-1 truncate text-xs text-wk-parchment/50">
                  {describeDays(sc.days)}
                  {sc.warningMinutes.length > 0 ? (
                    <>
                      {" · warns at "}
                      <span className="font-mono">{sc.warningMinutes.map((m) => `−${m}m`).join(" ")}</span>
                    </>
                  ) : (
                    " · no warnings"
                  )}
                </p>
              </div>
              {canEdit && (
                <div className="ml-auto flex shrink-0 items-center gap-1.5">
                  <Switch
                    checked={sc.enabled}
                    disabled={toggle.isPending}
                    onCheckedChange={() => toggle.mutate(sc)}
                    aria-label={sc.enabled ? "Turn schedule off" : "Turn schedule on"}
                  />
                  <button
                    className="rounded p-1.5 text-wk-parchment/40 hover:bg-wk-parchment/5 hover:text-wk-parchment"
                    title="Edit schedule"
                    onClick={() => onEdit(sc)}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </button>
                  <button
                    className="rounded p-1.5 text-wk-parchment/40 hover:bg-wk-parchment/5 hover:text-destructive"
                    title="Remove schedule"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(sc.id)}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      <div className="space-y-1 border-t border-wk-edge px-5 py-3 text-xs text-wk-parchment/50">
        <p>
          Times are {data.timezone} (Wildskeeper's clock).{" "}
          {saveCmd.known && !saveCmd.supported ? (
            <>
              This server cannot be saved on demand — {saveCmd.reason} — so each restart costs whatever came after the
              game's last autosave. Every run records that in Activity.
            </>
          ) : (
            <>
              Wildskeeper saves the world before each restart, and records in Activity whether the save landed.
            </>
          )}
        </p>
        {!data.dockerRestart && (
          <p className="text-wk-parchment/60">
            No container is configured for this server, so a restart asks the game to shut down and relies on the
            container's restart policy to bring it back up.
          </p>
        )}
      </div>
    </section>
  );
}

function ScheduleDialog({
  serverId,
  schedule,
  open,
  onOpenChange,
}: {
  serverId: number;
  schedule: RestartSchedule | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [days, setDays] = useState<number[]>([]);
  const [timeOfDay, setTimeOfDay] = useState("05:00");
  const [warnings, setWarnings] = useState<number[]>([15, 5, 1]);

  // Re-seed the form whenever the dialog opens on a different schedule.
  useEffect(() => {
    if (!open) return;
    setDays(schedule?.days ?? [0, 1, 2, 3, 4, 5, 6]);
    setTimeOfDay(schedule?.timeOfDay ?? "05:00");
    setWarnings(schedule?.warningMinutes ?? [15, 5, 1]);
  }, [open, schedule]);

  const save = useMutation({
    mutationFn: () => {
      const input: ScheduleWriteInput = {
        enabled: schedule?.enabled ?? true,
        days,
        timeOfDay,
        warningMinutes: warnings,
      };
      return schedule ? api.updateSchedule(serverId, schedule.id, input) : api.createSchedule(serverId, input);
    },
    onSuccess: () => {
      toast.success(schedule ? "Schedule updated" : "Schedule added");
      queryClient.invalidateQueries({ queryKey: ["automation", serverId] });
      onOpenChange(false);
    },
    onError: (err) => toast.error("Could not save schedule", { description: errorDetail(err) }),
  });

  const toggleDay = (d: number) =>
    setDays((prev) => (prev.includes(d) ? prev.filter((x) => x !== d) : [...prev, d].sort()));
  const toggleWarning = (m: number) =>
    setWarnings((prev) => (prev.includes(m) ? prev.filter((x) => x !== m) : [...prev, m].sort((a, b) => b - a)));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{schedule ? "Edit schedule" : "Add restart schedule"}</DialogTitle>
          <DialogDescription>
            Each lead time sends a Discord warning; an in-game warning needs a dwbridge mod that can broadcast, and
            none does yet. At the scheduled time Wildskeeper saves the world, then restarts the server.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label className="text-xs">Days</Label>
            <div className="flex gap-1.5">
              {DAY_LETTERS.map((letter, i) => (
                <button
                  key={i}
                  type="button"
                  aria-pressed={days.includes(i)}
                  className={cn(
                    "flex h-9 w-9 items-center justify-center rounded-full font-display text-sm font-bold transition-colors",
                    days.includes(i)
                      ? "bg-wk-ember text-wk-parchment"
                      : "border border-wk-edge text-wk-parchment/40 hover:border-wk-edge hover:text-wk-parchment",
                  )}
                  onClick={() => toggleDay(i)}
                >
                  {letter}
                </button>
              ))}
            </div>
            {days.length === 0 && <p className="text-xs text-destructive">Pick at least one day.</p>}
          </div>

          <div className="w-32 space-y-1.5">
            <Label className="text-xs">Restart at</Label>
            <Input type="time" value={timeOfDay} onChange={(e) => setTimeOfDay(e.target.value)} />
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">Warn players before</Label>
            <div className="flex flex-wrap gap-1.5">
              {WARNING_PRESETS.map((m) => (
                <button
                  key={m}
                  type="button"
                  aria-pressed={warnings.includes(m)}
                  className={cn(
                    "rounded-full px-3 py-1.5 font-mono text-xs transition-colors",
                    warnings.includes(m)
                      ? "bg-wk-brasshi/25 text-wk-parchment ring-1 ring-wk-brasshi"
                      : "border border-wk-edge text-wk-parchment/40 hover:border-wk-edge hover:text-wk-parchment",
                  )}
                  onClick={() => toggleWarning(m)}
                >
                  {m} min
                </button>
              ))}
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={days.length === 0 || !timeOfDay || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? "Saving..." : schedule ? "Save changes" : "Add schedule"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function WatchdogCard({
  serverId,
  config,
}: {
  serverId: number;
  config: { enabled: boolean; available: boolean; supervised?: boolean };
}) {
  const queryClient = useQueryClient();

  const toggle = useMutation({
    mutationFn: (enabled: boolean) => api.setWatchdog(serverId, enabled),
    onSuccess: (res) => {
      toast.success(res.enabled ? "Watchdog on" : "Watchdog off");
      queryClient.invalidateQueries({ queryKey: ["automation", serverId] });
    },
    onError: (err) => toast.error("Could not update the watchdog", { description: errorDetail(err) }),
  });

  return (
    <section className="rounded-xl border border-wk-edge bg-wk-panel lg:col-span-3">
      <div className="flex items-center justify-between gap-3 border-b border-wk-edge px-5 py-4">
        <div className="flex items-center gap-2">
          <Dog className="h-4 w-4 text-wk-brasshi" />
          <h2 className="font-display text-base font-bold">Crash watchdog</h2>
        </div>
        <Switch
          checked={config.enabled}
          disabled={!config.available || toggle.isPending}
          onCheckedChange={(on) => toggle.mutate(on)}
          aria-label={config.enabled ? "Turn the watchdog off" : "Turn the watchdog on"}
        />
      </div>
      <div className="space-y-2 px-5 py-4 text-sm text-wk-parchment/60">
        <p>
          Restarts the container when it exits with an error — a crash, an out-of-memory kill — checking every 30
          seconds. After three restarts in a row it stands down until the server stays up for 10 minutes or someone
          steps in, so a server that crashes on boot isn't bounced forever. Watchdog restarts appear in Activity and
          in Discord status notifications.
        </p>
        <p className="text-xs text-wk-parchment/45">
          Clean stops are left alone — stopping through Wildskeeper always reads as one. Stopping the container behind
          Wildskeeper's back (TrueNAS UI, docker stop) can end in a force-kill that looks like a crash and will be revived;
          turn the watchdog off first, or stop the server from here.
        </p>
        {!config.available && (
          <p className="text-xs text-wk-parchment/60">
            {config.supervised
              ? "This server's agent already restarts the game when it crashes, with the same backoff — the container itself never exits, so there's nothing here to watch."
              : "Needs power control: a Docker endpoint on this Wildskeeper instance and a container name on this server."}
          </p>
        )}
      </div>
    </section>
  );
}

const BACKUP_INTERVALS = [
  { hours: 0, label: "No schedule" },
  { hours: 1, label: "Every hour" },
  { hours: 6, label: "Every 6 hours" },
  { hours: 12, label: "Every 12 hours" },
  { hours: 24, label: "Daily" },
  { hours: 48, label: "Every 2 days" },
  { hours: 168, label: "Weekly" },
];
const BACKUP_KEEPS = [7, 14, 30];

function fmtBytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KB`;
  return `${n} B`;
}

function BackupsCard({ serverId }: { serverId: number }) {
  const queryClient = useQueryClient();
  const backupsQuery = useQuery({
    queryKey: ["backups", serverId],
    queryFn: () => api.listBackups(serverId),
    // Poll fast while a snapshot is being written so it appears when done.
    refetchInterval: (query) => (query.state.data?.running ? 2000 : 30_000),
  });
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["backups", serverId] });

  const settings = useMutation({
    mutationFn: (next: { intervalHours: number; keep: number }) =>
      api.setBackupSettings(serverId, next.intervalHours, next.keep),
    onSuccess: () => {
      toast.success("Backup schedule saved");
      invalidate();
    },
    onError: (err) => toast.error("Could not save backup settings", { description: errorDetail(err) }),
  });

  const run = useMutation({
    mutationFn: () => api.runBackup(serverId),
    onSuccess: () => {
      toast.success("Backup started");
      invalidate();
    },
    onError: (err) => toast.error("Could not start a backup", { description: errorDetail(err) }),
  });

  const remove = useMutation({
    mutationFn: (name: string) => api.deleteBackup(serverId, name),
    onSuccess: () => {
      toast.success("Snapshot deleted");
      invalidate();
    },
    onError: (err) => toast.error("Could not delete the snapshot", { description: errorDetail(err) }),
  });

  const data = backupsQuery.data;

  return (
    <section className="rounded-xl border border-wk-edge bg-wk-panel lg:col-span-5">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-wk-edge px-5 py-4">
        <div className="flex items-center gap-2">
          <HardDriveDownload className="h-4 w-4 text-wk-ok" />
          <h2 className="font-display text-base font-bold">Save backups</h2>
        </div>
        {data?.available && (
          <Button size="sm" disabled={data.running || run.isPending} onClick={() => run.mutate()}>
            {data.running ? "Backing up…" : "Back up now"}
          </Button>
        )}
      </div>

      <div className="space-y-4 px-5 py-4">
        {data && !data.available && (
          <p className="text-sm text-wk-parchment/60">
            Backups snapshot the save directory, so this server needs a save path first (edit the server from the
            sidebar). The save mount stays read-only — snapshots are written to Wildskeeper's own data directory.
          </p>
        )}

        {data?.available && (
          <>
            <div className="flex flex-wrap items-end gap-3">
              <div className="space-y-1.5">
                {/* block: Label renders inline, and the Select wrapper is
                    inline-flex — without it they share a line, glued. */}
                <Label className="block text-xs">Schedule</Label>
                <Select
                  value={String(data.intervalHours)}
                  onChange={(e) => settings.mutate({ intervalHours: Number(e.target.value), keep: data.keep })}
                  className="w-44"
                >
                  {BACKUP_INTERVALS.map((o) => (
                    <option key={o.hours} value={o.hours}>
                      {o.label}
                    </option>
                  ))}
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label className="block text-xs">Keep</Label>
                <Select
                  value={String(data.keep)}
                  onChange={(e) => settings.mutate({ intervalHours: data.intervalHours, keep: Number(e.target.value) })}
                  className="w-40"
                >
                  {BACKUP_KEEPS.map((k) => (
                    <option key={k} value={k}>
                      Last {k} snapshots
                    </option>
                  ))}
                </Select>
              </div>
              <p className="pb-2 text-xs text-wk-parchment/45">
                Skips runs while the save hasn't changed. Restores are manual: stop the server, unpack a snapshot
                over the save, start it again.
              </p>
            </div>

            {data.snapshots.length === 0 ? (
              <p className="text-sm text-wk-parchment/60">
                No snapshots yet{data.running ? " — one is being written now." : "."}
              </p>
            ) : (
              <ul className="divide-y divide-wk-edge">
                {data.snapshots.map((snap) => (
                  <li key={snap.name} className="flex items-center gap-3 py-2 text-sm">
                    <span className="font-mono text-xs tabular-nums text-wk-parchment/70">
                      {new Date(snap.ts).toLocaleString([], {
                        year: "numeric",
                        month: "short",
                        day: "numeric",
                        hour: "2-digit",
                        minute: "2-digit",
                      })}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-xs text-wk-parchment/40">{agoLabel(snap.ts)}</span>
                    <span className="w-20 text-right font-mono text-xs tabular-nums text-wk-parchment/60">
                      {fmtBytes(snap.bytes)}
                    </span>
                    <a
                      href={api.backupDownloadURL(serverId, snap.name)}
                      className="rounded p-1.5 text-wk-parchment/40 hover:bg-wk-parchment/5 hover:text-wk-parchment"
                      title="Download snapshot"
                      download
                    >
                      <Download className="h-3.5 w-3.5" />
                    </a>
                    <button
                      className="rounded p-1.5 text-wk-parchment/40 hover:bg-wk-parchment/5 hover:text-destructive"
                      title="Delete snapshot"
                      disabled={remove.isPending}
                      onClick={() => remove.mutate(snap.name)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </li>
                ))}
              </ul>
            )}

            {data.snapshots.length > 0 && (
              <p className="text-xs text-wk-parchment/40">
                {data.snapshots.length} snapshot{data.snapshots.length === 1 ? "" : "s"} ·{" "}
                {fmtBytes(data.totalBytes)} on disk
              </p>
            )}
          </>
        )}
      </div>
    </section>
  );
}

function PublicStatusCard({ serverId, config }: { serverId: number; config: { enabled: boolean; token: string } }) {
  const queryClient = useQueryClient();
  const url = config.token ? `${window.location.origin}/status/${config.token}` : "";

  const toggle = useMutation({
    mutationFn: (enabled: boolean) => api.setPublicStatus(serverId, enabled),
    onSuccess: (res) => {
      toast.success(res.enabled ? "Status page is live" : "Status page taken down");
      queryClient.invalidateQueries({ queryKey: ["automation", serverId] });
    },
    onError: (err) => toast.error("Could not update the status page", { description: errorDetail(err) }),
  });

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      toast.success("Link copied");
    } catch {
      toast.error("Could not copy — select the link text instead");
    }
  };

  return (
    <section className="rounded-xl border border-wk-edge bg-wk-panel lg:col-span-2">
      <div className="flex items-center justify-between gap-3 border-b border-wk-edge px-5 py-4">
        <div className="flex items-center gap-2">
          <Globe className="h-4 w-4 text-wk-ok" />
          <h2 className="font-display text-base font-bold">Public status page</h2>
        </div>
        <Switch
          checked={config.enabled}
          disabled={toggle.isPending}
          onCheckedChange={(on) => toggle.mutate(on)}
          aria-label={config.enabled ? "Take the status page down" : "Publish the status page"}
        />
      </div>
      <div className="space-y-3 px-5 py-4 text-sm text-wk-parchment/60">
        <p>
          A read-only page anyone with the link can open, no account needed: online or not, player count, and the
          next scheduled restart. Served from Wildskeeper's own data — visitors never touch the game server. No player
          names, no addresses.
        </p>
        {config.enabled && url && (
          <div className="flex items-center gap-2">
            <a
              href={url}
              target="_blank"
              rel="noreferrer"
              className="min-w-0 flex-1 truncate rounded-lg border border-wk-edge bg-wk-ink/[0.03] px-2.5 py-1.5 font-mono text-xs text-wk-parchment/70 hover:text-wk-parchment"
              title={url}
            >
              {url}
            </a>
            <Button variant="outline" size="sm" onClick={copy}>
              <Copy className="h-3.5 w-3.5" />
              Copy
            </Button>
          </div>
        )}
        {config.enabled && (
          <p className="text-xs text-wk-parchment/45">
            The link is the only key — anyone holding it can view the page. Turning it off and on again mints a new
            link and kills the old one.
          </p>
        )}
      </div>
    </section>
  );
}

function DiscordCard({ serverId, config }: { serverId: number; config: DiscordConfig }) {
  const queryClient = useQueryClient();
  const [url, setUrl] = useState("");
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["automation", serverId] });

  const saveUrl = useMutation({
    mutationFn: () =>
      api.setDiscord(serverId, {
        webhookUrl: url.trim(),
        // A fresh webhook starts with everything on; replacing the URL on
        // an existing one keeps the toggles as they are.
        enabled: config.configured ? config.enabled : true,
        onStatus: config.configured ? config.onStatus : true,
        onPlayers: config.configured ? config.onPlayers : true,
        onRestarts: config.configured ? config.onRestarts : true,
      }),
    onSuccess: () => {
      toast.success(config.configured ? "Webhook replaced" : "Webhook saved");
      setUrl("");
      invalidate();
    },
    onError: (err) => toast.error("Could not save webhook", { description: errorDetail(err) }),
  });

  const saveToggles = useMutation({
    mutationFn: (next: DiscordConfig) =>
      api.setDiscord(serverId, {
        webhookUrl: "",
        enabled: next.enabled,
        onStatus: next.onStatus,
        onPlayers: next.onPlayers,
        onRestarts: next.onRestarts,
      }),
    onSuccess: invalidate,
    onError: (err) => {
      toast.error("Could not save notification settings", { description: errorDetail(err) });
      invalidate();
    },
  });

  const test = useMutation({
    mutationFn: () => api.testDiscord(serverId),
    onSuccess: () => toast.success("Test message sent", { description: "Check the Discord channel." }),
    onError: (err) => toast.error("Test message failed", { description: errorDetail(err) }),
  });

  const remove = useMutation({
    mutationFn: () => api.deleteDiscord(serverId),
    onSuccess: () => {
      toast.success("Webhook removed");
      invalidate();
    },
    onError: (err) => toast.error("Could not remove webhook", { description: errorDetail(err) }),
  });

  const events: { key: "onStatus" | "onPlayers" | "onRestarts"; label: string; help: string }[] = [
    { key: "onStatus", label: "Server status", help: "Unreachable and back online" },
    { key: "onPlayers", label: "Players", help: "Joins and leaves" },
    { key: "onRestarts", label: "Restarts", help: "Scheduled restart warnings" },
  ];

  return (
    <section className="rounded-xl border border-wk-edge bg-wk-panel lg:col-span-2">
      <div className="flex items-center justify-between border-b border-wk-edge px-5 py-4">
        <div className="flex items-center gap-2">
          <Webhook className="h-4 w-4 text-wk-rune" />
          <h2 className="font-display text-base font-bold">Discord notifications</h2>
        </div>
        {config.configured && (
          <Switch
            checked={config.enabled}
            disabled={saveToggles.isPending}
            onCheckedChange={(enabled) => saveToggles.mutate({ ...config, enabled })}
            aria-label={config.enabled ? "Turn notifications off" : "Turn notifications on"}
          />
        )}
      </div>

      <div className="space-y-4 px-5 py-4">
        {config.configured ? (
          <div className="flex items-center gap-2 text-sm text-wk-parchment/70">
            <span className="h-2 w-2 rounded-full bg-wk-rune" />
            Webhook connected
          </div>
        ) : (
          <p className="text-sm text-wk-parchment/60">
            Post server events to a Discord channel. In Discord: Server Settings → Integrations → Webhooks → New
            Webhook, then copy its URL here.
          </p>
        )}

        <div className="space-y-1.5">
          <Label className="text-xs">{config.configured ? "Replace webhook URL" : "Webhook URL"}</Label>
          <div className="flex gap-2">
            <Input
              type="password"
              placeholder="https://discord.com/api/webhooks/…"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
            />
            <Button disabled={!url.trim() || saveUrl.isPending} onClick={() => saveUrl.mutate()}>
              Save
            </Button>
          </div>
        </div>

        {config.configured && (
          <>
            <ul className={cn("space-y-2.5", !config.enabled && "opacity-50")}>
              {events.map((ev) => (
                <li key={ev.key} className="flex items-center justify-between gap-3">
                  <div>
                    <p className="text-sm font-medium">{ev.label}</p>
                    <p className="text-xs text-wk-parchment/50">{ev.help}</p>
                  </div>
                  <Switch
                    checked={config[ev.key]}
                    disabled={!config.enabled || saveToggles.isPending}
                    onCheckedChange={(on) => saveToggles.mutate({ ...config, [ev.key]: on })}
                    aria-label={`${ev.label} notifications`}
                  />
                </li>
              ))}
            </ul>

            <div className="flex items-center justify-between border-t border-wk-edge pt-3">
              <Button variant="outline" size="sm" disabled={test.isPending} onClick={() => test.mutate()}>
                <Send className="h-3.5 w-3.5" />
                {test.isPending ? "Sending..." : "Send test message"}
              </Button>
              <button
                className="text-xs text-wk-parchment/40 underline-offset-2 hover:text-destructive hover:underline"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                Remove webhook
              </button>
            </div>
          </>
        )}
      </div>
    </section>
  );
}
