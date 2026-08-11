import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eraser, HardDriveDownload, Play, RotateCw, Save, ScrollText, Square } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError, errorDetail } from "../lib/api";
import { useAuth } from "../lib/auth";
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
                servers, where the bridge is the only lever. Docker knowing the
                container is down is the one case worth disabling for; in
                agent-managed mode the honest 501/502 speaks instead. */}
            {canSave && (
              <Button
                variant="secondary"
                size="sm"
                disabled={save.isPending || (!powerOff && powerAvailable && !running)}
                title={
                  !powerOff && powerAvailable && !running
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
                {confirming !== "start" && canSave && (
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
