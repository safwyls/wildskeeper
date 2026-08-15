import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eraser, HardDriveDownload, Play, RotateCw, Save, ScrollText, Square } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError, errorDetail, LAUNCH_PROFILES } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useCommand } from "../lib/capabilities";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";
import { ContainerLogsDialog } from "./ContainerLogsDialog";
import { SteamJobLogDialog } from "./SteamJobLogDialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";

type Action = "start" | "stop" | "restart";

const CONFIRM: Record<Action, { title: string; body: string; verb: string }> = {
  start: { title: "Start the server?", body: "The container will boot and players can connect once it's up.", verb: "Start" },
  stop: {
    title: "Stop the server?",
    body: "Anyone playing will be disconnected. The stop is graceful, but the game does not save on shutdown — anything since the last autosave is lost.",
    verb: "Stop",
  },
  restart: {
    title: "Restart the server?",
    body: "Anyone playing will be disconnected and the server will come straight back up. The game does not save on shutdown — anything since the last autosave is lost.",
    verb: "Restart",
  },
};

/**
 * Start/stop/restart the container the game server runs in.
 *
 * Renders nothing unless the instance has a Docker endpoint and this server
 * has a container name — power control is optional, and a server without it
 * should look no different from before the feature existed.
 */
export function ServerPower({
  serverId,
  installPath,
  agentUrl,
}: {
  serverId: number;
  installPath?: string;
  agentUrl?: string;
}) {
  const { can } = useAuth();
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<Action | null>(null);
  const [logsOpen, setLogsOpen] = useState(false);
  const [cacheConfirmOpen, setCacheConfirmOpen] = useState(false);
  const [updateConfirmOpen, setUpdateConfirmOpen] = useState(false);
  const [jobLogOpen, setJobLogOpen] = useState(false);

  const statusQuery = useQuery({
    queryKey: ["container", serverId],
    queryFn: () => api.containerStatus(serverId),
    retry: false,
    refetchInterval: 15_000,
  });

  const act = useMutation({
    mutationFn: (action: Action) => api.containerAction(serverId, action),
    onSuccess: (_, action) => {
      toast.success(`Server ${action === "stop" ? "stopped" : action === "start" ? "started" : "restarted"}`);
      setConfirming(null);
      queryClient.invalidateQueries({ queryKey: ["container", serverId] });
      // The game server takes a moment to accept connections, so let the
      // reachability probes re-run shortly after.
      setTimeout(() => {
        queryClient.invalidateQueries({ queryKey: ["server-info", serverId] });
        queryClient.invalidateQueries({ queryKey: ["container", serverId] });
      }, 5000);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Action failed"),
  });

  // On-demand world save, through the dwbridge mod. Support is discovered by
  // doing: a game with no bridge answers 501 with its own explanation, which
  // the toast relays — so the button shows for anyone with the save grant
  // rather than guessing at capability up front.
  const save = useMutation({
    mutationFn: () => api.save(serverId),
    onSuccess: () => {
      toast.success("World saved");
      // The proof is the world panel itself: "Last written" ticks to just
      // now and the save revision counts up.
      queryClient.invalidateQueries({ queryKey: ["world", serverId] });
    },
    onError: (err) =>
      toast.error("Save failed", {
        description: errorDetail(err) ?? (err instanceof Error ? err.message : undefined),
      }),
  });

  const allowed = can("power");
  const canSave = can("save");
  // Whether this server can save at all, asked rather than assumed. Only
  // worth asking for someone who could use the answer.
  const saveCmd = useCommand(serverId, "save", canSave);
  const saveBlocked = saveCmd.known && !saveCmd.supported;

  // Polls only while the agent reports a running job; also runs once on
  // mount, so a reload (or a wildskeeper restart) rediscovers an in-flight
  // update instead of forgetting it.
  const updateStatus = useQuery({
    queryKey: ["steam-update", serverId],
    queryFn: () => api.steamUpdateStatus(serverId),
    enabled: !!agentUrl && allowed,
    retry: false,
    refetchInterval: (q) => (q.state.data?.job?.state === "running" ? 2000 : false),
  });
  const job = updateStatus.data?.job ?? null;
  const jobRunning = job?.state === "running";

  // Announce the running → settled transition. A ref rather than state:
  // this is bookkeeping about what was already shown, not render input.
  const prevJobState = useRef<string | null>(null);
  useEffect(() => {
    const state = job?.state ?? null;
    if (prevJobState.current === "running" && state && state !== "running") {
      if (state === "done") {
        toast.success("Server updated — start it back up when ready");
      } else {
        toast.error(`Update failed: ${job?.error || "see the agent's job log"}`);
      }
    }
    prevJobState.current = state;
  }, [job?.state, job?.error]);

  const startUpdate = useMutation({
    mutationFn: () => api.steamUpdateStart(serverId),
    onSuccess: () => {
      toast.success("Update started");
      setUpdateConfirmOpen(false);
      queryClient.invalidateQueries({ queryKey: ["steam-update", serverId] });
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to start the update"),
  });

  const clearCache = useMutation({
    mutationFn: () => api.clearSteamCache(serverId),
    onSuccess: ({ removed }) => {
      toast.success(
        removed === 0
          ? "SteamCMD cache was already empty"
          : "SteamCMD cache cleared — restart the server to re-download",
      );
      setCacheConfirmOpen(false);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to clear the SteamCMD cache"),
  });

  // Not configured (400) is the normal "docker power is off" case — but an
  // agent-managed server still has repair tools to show, so only a server
  // with neither configured hides the card entirely.
  if (
    statusQuery.isError &&
    statusQuery.error instanceof ApiError &&
    statusQuery.error.status === 400 &&
    !agentUrl
  ) {
    return null;
  }
  if (statusQuery.isLoading) return null;

  const state = statusQuery.data;
  const running = state?.running ?? false;
  // Docker power deliberately not configured (400) is a designed state for
  // agent-managed servers, not an error — the power row hides rather than
  // rendering four permanently dead buttons. Transient failures (proxy
  // down) keep the row, disabled, so the feature doesn't vanish.
  const powerOff =
    statusQuery.isError && statusQuery.error instanceof ApiError && statusQuery.error.status === 400;
  const powerAvailable = !statusQuery.isError;
  const agentAlive = !!updateStatus.data;

  // A SteamCMD job with the game down is the deploy/repair window — the
  // moment a provisioned server is installing itself.
  const settingUp = jobRunning && !running;
  // Agent configured but unreachable: either the stack is still coming up
  // (deploying) or it's down — indeterminate, shown as a pulse, not an
  // error.
  const agentPending = !!agentUrl && !agentAlive && updateStatus.isError;

  return (
    <section className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-wk-edge bg-wk-panel p-4 lg:p-5">
      <div className="flex items-center gap-3">
        <span
          className={cn(
            "h-2.5 w-2.5 shrink-0 rounded-full",
            settingUp
              ? "animate-pulse bg-wk-brasshi"
              : running || (powerOff && agentAlive)
                ? "bg-wk-ok"
                : agentPending
                  ? "animate-pulse bg-wk-parchment/30"
                  : "bg-wk-parchment/30",
          )}
          aria-hidden
        />
        <div>
          <p className="font-display text-sm font-bold">
            {settingUp
              ? "Setting up — SteamCMD working"
              : powerOff
                ? "Agent-managed"
                : `Container ${running ? "running" : (state?.status ?? "unknown")}`}
          </p>
          <p className="font-mono text-xs text-wk-parchment/40">
            {powerOff ? (
              <>
                {agentAlive ? "wkagent connected" : "wkagent unreachable — deploying, or the stack is down"} ·
                docker power control not configured
              </>
            ) : (
              <>
                {state?.name}
                {statusQuery.isError && " · status unavailable"}
              </>
            )}
          </p>
        </div>
      </div>

      {/* Five nowrap buttons are wider than a phone, so the cluster wraps —
          but at the seam the actions already have rather than wherever the
          line happens to run out. The two groups below are atomic: the
          process trio moves to its own line intact before any button inside
          it breaks away. */}
      {(!powerOff || canSave) && (
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:justify-end">
          {!powerOff && !allowed && (
            <span className="text-xs text-wk-parchment/40">You don't have power permission</span>
          )}
          <div className="flex flex-wrap items-center gap-2">
            {/* Logs share the power grant — same gate as the endpoint. */}
            {!powerOff && allowed && (
              <Button variant="secondary" size="sm" disabled={!powerAvailable} onClick={() => setLogsOpen(true)}>
                <ScrollText className="h-4 w-4" />
                Logs
              </Button>
            )}
            {/* A world action, not a process action, so it sits ahead of the
                power group — and unlike them it stays for agent-managed
                servers, where the bridge is the only lever. Disabled for the
                two cases we can know about without asking the game: docker
                reporting the container down, and the capability probe saying
                this server has no way to save. Anything else stays clickable
                and explains itself in the toast. */}
            {canSave && (
              <Button
                variant="secondary"
                size="sm"
                disabled={save.isPending || (!powerOff && powerAvailable && !running) || saveBlocked}
                title={
                  saveBlocked
                    ? saveCmd.reason
                    : !powerOff && powerAvailable && !running
                      ? "The server is not running"
                      : "Ask the game to write the world to disk now"
                }
                onClick={() => save.mutate()}
              >
                <Save className="h-4 w-4" />
                {save.isPending ? "Saving…" : "Save world"}
              </Button>
            )}
          </div>
          {!powerOff && (
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="secondary"
                size="sm"
                disabled={!allowed || !powerAvailable || running || act.isPending}
                onClick={() => setConfirming("start")}
              >
                <Play className="h-4 w-4" />
                Start
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={!allowed || !powerAvailable || !running || act.isPending}
                onClick={() => setConfirming("restart")}
              >
                <RotateCw className="h-4 w-4" />
                Restart
              </Button>
              <Button
                variant="destructive"
                size="sm"
                disabled={!allowed || !powerAvailable || !running || act.isPending}
                onClick={() => setConfirming("stop")}
              >
                <Square className="h-4 w-4" />
                Stop
              </Button>
            </div>
          )}
        </div>
      )}

      {/* Launch mode sits directly above SteamCMD because that is the order
          the work happens in: choose the build, then install it. */}
      {agentUrl && <LaunchMode serverId={serverId} canEdit={allowed} />}

      {/* Maintenance strip: repair tools, not routine actions, so they sit
          below the power row rather than crowding it. Hidden entirely when
          neither an agent nor an install path is configured — same principle
          as the card itself. */}
      {allowed && (agentUrl || installPath) && (
        <div className="flex w-full flex-wrap items-center justify-between gap-3 border-t border-wk-edge pt-3">
          <div>
            <p className="font-display text-sm font-bold">SteamCMD</p>
            <p className="text-xs text-wk-parchment/40">
              {jobRunning
                ? "Updating the server install — this can take several minutes."
                : "Stuck after a Dragonwilds patch? Clear the cache or re-run the update."}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {agentUrl && (
              <Button
                variant="secondary"
                size="sm"
                disabled={jobRunning || running || startUpdate.isPending}
                title={running ? "Stop the server first" : undefined}
                onClick={() => setUpdateConfirmOpen(true)}
              >
                <HardDriveDownload className={cn("h-4 w-4", jobRunning && "animate-pulse")} />
                {jobRunning ? "Updating…" : "Update server"}
              </Button>
            )}
            <Button
              variant="secondary"
              size="sm"
              disabled={clearCache.isPending || jobRunning}
              onClick={() => setCacheConfirmOpen(true)}
            >
              <Eraser className="h-4 w-4" />
              Clear cache
            </Button>
            {/* The agent keeps the last job's tail, so the log stays
                readable after completion — not only mid-run. */}
            {job && (
              <Button variant="secondary" size="sm" onClick={() => setJobLogOpen(true)}>
                <ScrollText className="h-4 w-4" />
                Update log
              </Button>
            )}
          </div>
        </div>
      )}

      <SteamJobLogDialog job={job} open={jobLogOpen} onOpenChange={setJobLogOpen} />

      <ContainerLogsDialog
        serverId={serverId}
        containerName={state?.name ?? ""}
        open={logsOpen}
        onOpenChange={setLogsOpen}
      />

      <Dialog open={updateConfirmOpen} onOpenChange={setUpdateConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Update the server install?</DialogTitle>
            <DialogDescription>
              The agent runs SteamCMD update with validation against the install directory — the
              repair for a broken game update. This can take several minutes; keep the server
              stopped until it finishes.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setUpdateConfirmOpen(false)}>
              Cancel
            </Button>
            <Button disabled={startUpdate.isPending} onClick={() => startUpdate.mutate()}>
              {startUpdate.isPending ? "Starting…" : "Update server"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={cacheConfirmOpen} onOpenChange={setCacheConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Clear the SteamCMD cache?</DialogTitle>
            <DialogDescription>
              Deletes everything inside <code className="font-mono">steamapps/</code> and{" "}
              <code className="font-mono">steam/packages/</code> under the install directory. Game
              files and saves are untouched — SteamCMD re-validates and re-downloads on the next
              server start.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCacheConfirmOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" disabled={clearCache.isPending} onClick={() => clearCache.mutate()}>
              {clearCache.isPending ? "Clearing…" : "Clear cache"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirming !== null} onOpenChange={(open) => !open && setConfirming(null)}>
        <DialogContent>
          {confirming && (
            <>
              <DialogHeader>
                <DialogTitle>{CONFIRM[confirming].title}</DialogTitle>
                <DialogDescription>{CONFIRM[confirming].body}</DialogDescription>
              </DialogHeader>
              {/* The footer stacks on a phone, where three buttons with no
                  gap read as one control; the desktop row keeps the
                  primitive's own spacing. */}
              <DialogFooter className="gap-2 sm:gap-0">
                <Button variant="outline" onClick={() => setConfirming(null)}>
                  Cancel
                </Button>
                <Button
                  variant={confirming === "stop" ? "destructive" : "default"}
                  disabled={act.isPending || save.isPending}
                  onClick={() => act.mutate(confirming)}
                >
                  {act.isPending ? "Working…" : CONFIRM[confirming].verb}
                </Button>
                {/* The warning above carries its own remedy: save first, and
                    only go down if the save landed. A refused save (no
                    dwbridge, agent down) toasts its reason and leaves the
                    dialog open — plain Stop/Restart is still right there.
                    Last in the footer so it takes the primary position in
                    both directions the footer lays out: rightmost on a
                    desktop row, topmost in the phone's reversed stack. The
                    warning's own answer should not sit below the action it
                    warns about. */}
                {confirming !== "start" && canSave && !saveBlocked && (
                  <Button
                    variant="secondary"
                    disabled={act.isPending || save.isPending}
                    onClick={() =>
                      save
                        .mutateAsync()
                        .then(() => act.mutate(confirming))
                        .catch(() => {})
                    }
                  >
                    {save.isPending ? "Saving…" : `Save world, then ${confirming}`}
                  </Button>
                )}
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </section>
  );
}

/**
 * Which of the game's two builds the agent launches.
 *
 * This is the setting the rest of the console reads capability from: the
 * native Linux build cannot load UE4SS, so a server on it will never save
 * on demand however healthy it looks. Switching is not a toggle — the two
 * builds come from different Steam depots — so the choice is confirmed, and
 * the consequences (re-download, restart) are stated before it is made
 * rather than discovered afterwards.
 */
function LaunchMode({ serverId, canEdit }: { serverId: number; canEdit: boolean }) {
  const { isAdmin } = useAuth();
  const queryClient = useQueryClient();
  const [switchTo, setSwitchTo] = useState<string | null>(null);
  const [rebuildOpen, setRebuildOpen] = useState(false);
  const [installOpen, setInstallOpen] = useState(false);

  const launchQuery = useQuery({
    queryKey: ["launch", serverId],
    queryFn: () => api.serverLaunch(serverId),
    retry: false,
    staleTime: 30_000,
  });

  const select = useMutation({
    mutationFn: (profile: string) => api.setServerLaunch(serverId, profile),
    onSuccess: (launch) => {
      toast.success(`Next start uses ${LAUNCH_PROFILES[launch.profile]?.label ?? launch.profile}`, {
        description: launch.installed
          ? "Restart the server to switch."
          : "Run Update server to download this build, then start.",
      });
      setSwitchTo(null);
      queryClient.invalidateQueries({ queryKey: ["launch", serverId] });
      // Whether commands can work follows directly from the build.
      queryClient.invalidateQueries({ queryKey: ["capabilities", serverId] });
    },
    onError: (err) => toast.error("Could not change the launch mode", { description: errorDetail(err) }),
  });

  // Provisioned agent containers belong to no orchestrator — they are not
  // in a TrueNAS apps list or a compose file — so the provisioner that made
  // them is the only thing that can move them to another image without
  // hand-written docker on the host.
  const rebuild = useMutation({
    mutationFn: (imageTag: string) => api.recreateAgent(serverId, imageTag),
    onSuccess: (res) => {
      toast.success("Agent rebuilt", { description: `Now running ${res.image}` });
      setRebuildOpen(false);
      // The agent is a new container: everything read from it is stale.
      for (const key of ["launch", "capabilities", "steam-update", "container", "server-info"]) {
        queryClient.invalidateQueries({ queryKey: [key, serverId] });
      }
    },
    onError: (err) => toast.error("Could not rebuild the agent", { description: errorDetail(err) }),
  });

  // One-click mod support: the agent copies the UE4SS+dwbridge kit baked
  // into its Wine image next to the server exe. Offered exactly when the
  // agent says it can act (kit present, modded build selected, nothing
  // installed yet) — every other state renders as text, not a dead button.
  const installBridge = useMutation({
    mutationFn: () => api.installBridge(serverId),
    onSuccess: (res) => {
      toast.success("Mod support installed", {
        description: res.restartRequired
          ? "The mod loads at process start — restart the server to activate it."
          : "It will load when the server starts.",
      });
      setInstallOpen(false);
      queryClient.invalidateQueries({ queryKey: ["launch", serverId] });
      queryClient.invalidateQueries({ queryKey: ["capabilities", serverId] });
    },
    onError: (err) => toast.error("Could not install mod support", { description: errorDetail(err) }),
  });

  const launch = launchQuery.data;
  // A companion-mode agent (400) has no build to choose, and a server whose
  // agent is down shouldn't grow a broken control — in both cases the row
  // simply isn't there.
  if (!launch?.profile) return null;
  const options = launch.available ?? [];

  return (
    <div className="flex w-full flex-wrap items-center justify-between gap-3 border-t border-wk-edge pt-3">
      <div className="min-w-0">
        <p className="font-display text-sm font-bold">Launch mode</p>
        <p className="text-xs text-wk-parchment/40">
          {launch.runnable === false
            ? "This agent's image has no Wine in it, so this build cannot start. Rebuild the agent on the Wine image."
            : launch.pendingRestart
            ? `Running the previous build — restart to switch to ${LAUNCH_PROFILES[launch.profile]?.label ?? launch.profile}.`
            : !launch.installed
              ? "This build isn't downloaded yet — run Update server below, then start."
              : (LAUNCH_PROFILES[launch.profile]?.blurb ??
                (launch.mods ? "Can run the dwbridge mod." : "No mod support."))}
        </p>
        {launch.runnable === false && isAdmin && (
          <button
            type="button"
            onClick={() => setRebuildOpen(true)}
            className="mt-1 text-xs font-semibold text-wk-brasshi underline-offset-2 hover:underline"
          >
            Rebuild agent on the Wine image →
          </button>
        )}
        {/* The last mile from "the Windows build runs" to "commands work":
            the agent lays its baked-in UE4SS+dwbridge kit next to the exe.
            Gated on the agent's own report so the button can only appear
            when the call would succeed. */}
        {launch.mods && launch.runnable !== false && launch.installed && launch.bridgeKit && !launch.bridgeInstalled && canEdit && (
          <button
            type="button"
            onClick={() => setInstallOpen(true)}
            className="mt-1 text-xs font-semibold text-wk-brasshi underline-offset-2 hover:underline"
          >
            Install mod support →
          </button>
        )}
      </div>
      {options.length > 1 && (
        <div className="flex items-center gap-1 rounded-lg bg-wk-ink p-1">
          {options.map((profile) => {
            const active = profile === launch.profile;
            return (
              <button
                key={profile}
                type="button"
                aria-pressed={active}
                disabled={!canEdit || select.isPending}
                onClick={() => !active && setSwitchTo(profile)}
                className={cn(
                  "rounded-md px-3 py-1 text-xs font-semibold transition disabled:opacity-50",
                  active ? "bg-wk-ember text-wk-parchment" : "text-wk-parchment/60 hover:text-wk-parchment",
                )}
              >
                {LAUNCH_PROFILES[profile]?.label ?? profile}
              </button>
            );
          })}
        </div>
      )}

      <Dialog open={rebuildOpen} onOpenChange={(open) => !open && setRebuildOpen(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rebuild the agent on the Wine image?</DialogTitle>
            <DialogDescription>
              Wildskeeper stops this server, removes its agent container and creates it again from
              <code className="mx-1 font-mono">wkagent:latest-wine</code>, keeping the same settings, ports and data
              directory. Your world and configuration live in the data directory and are not touched. The image is
              large, so the pull can take several minutes.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2 sm:gap-0">
            <Button variant="outline" onClick={() => setRebuildOpen(false)}>
              Cancel
            </Button>
            <Button disabled={rebuild.isPending} onClick={() => rebuild.mutate("latest-wine")}>
              {rebuild.isPending ? "Rebuilding…" : "Rebuild agent"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={installOpen} onOpenChange={(open) => !open && setInstallOpen(false)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Install mod support?</DialogTitle>
            <DialogDescription>
              Wildskeeper copies the proven UE4SS loader and the dwbridge mod from the agent&apos;s image into the
              game install, next to the server executable. This is what makes on-demand saves (and any future
              commands) work. Your world and settings are untouched, and nothing already installed is overwritten.
              The mod loads when the game process starts, so a running server needs a restart afterwards.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2 sm:gap-0">
            <Button variant="outline" onClick={() => setInstallOpen(false)}>
              Cancel
            </Button>
            <Button disabled={installBridge.isPending} onClick={() => installBridge.mutate()}>
              {installBridge.isPending ? "Installing…" : "Install mod support"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={switchTo !== null} onOpenChange={(open) => !open && setSwitchTo(null)}>
        <DialogContent>
          {switchTo && (
            <>
              <DialogHeader>
                <DialogTitle>Switch to the {LAUNCH_PROFILES[switchTo]?.label ?? switchTo} build?</DialogTitle>
                <DialogDescription>
                  {LAUNCH_PROFILES[switchTo]?.blurb} The two builds come from different Steam depots, so the game
                  files have to be downloaded again with Update server before this one will start. Your world saves
                  and settings are untouched. The change takes effect at the next start, not now.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter className="gap-2 sm:gap-0">
                <Button variant="outline" onClick={() => setSwitchTo(null)}>
                  Cancel
                </Button>
                <Button disabled={select.isPending} onClick={() => select.mutate(switchTo)}>
                  {select.isPending ? "Switching…" : "Use this build"}
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
