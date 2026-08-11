import { useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, errorDetail, type WorldInfo } from "../../lib/api";
import { useAuth } from "../../lib/auth";
import { useCommand } from "../../lib/capabilities";
import { agoLabel } from "../../lib/time";
import { WkNote, WkPanel } from "../../components/wildskeeper/WkPanel";

function sizeLabel(bytes: number): string {
  if (bytes >= 1 << 30) return `${(bytes / (1 << 30)).toFixed(1)} GB`;
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

/** One fact in the world ledger: a small-caps label over a plain value. */
function WorldFact({ label, value, title }: { label: string; value: React.ReactNode; title?: string }) {
  return (
    <div>
      <div className="text-[11px] uppercase tracking-[0.14em] text-wk-mist">{label}</div>
      <div className="mt-0.5 text-sm text-wk-parchment" title={title}>
        {value}
      </div>
    </div>
  );
}

/** The world as the save file records it: nameplate, ledger, and the raw
 * header values the recon has no verified labels for, kept as-recorded. */
function WorldPanel({ world }: { world: WorldInfo }) {
  return (
    <WkPanel title="The world" meta={<span className="font-mono">{world.file}</span>}>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <div className="font-wkdisplay text-2xl font-semibold text-wk-parchment">{world.worldName}</div>
          <div className="mt-1 text-xs text-wk-mist">
            {world.mapName}
            {world.ownerName && <> · kept by {world.ownerName}</>}
          </div>
          <div className="mt-1.5 font-mono text-[11px] tracking-[0.06em] text-wk-mist" title="WorldSaveGuid — the id this world goes by in the server's own log">
            {world.saveGuid}
          </div>
        </div>
        <div className="grid shrink-0 grid-cols-2 gap-x-8 gap-y-3 sm:text-right">
          <WorldFact
            label="Last written"
            value={agoLabel(world.modTime)}
            title={new Date(world.modTime).toLocaleString()}
          />
          <WorldFact label="Save revision" value={world.saveFileRevision} title="Counts up with every save the game makes" />
          <WorldFact label="Friendly fire" value={world.friendlyFire ? "On" : "Off"} />
          <WorldFact label="Crossplay" value={world.crossplayEnabled ? "On" : "Off"} />
        </div>
      </div>
      <div className="mt-3.5 rounded-sm bg-wk-ink px-3 py-2 font-mono text-[11px] text-wk-mist">
        as recorded in the save header · difficulty {world.survivalDifficulty} · hardcore {world.hardcoreState} ·
        privacy {world.sessionPrivacy} · {world.hasSessionPassword ? "password set" : "no password"} ·{" "}
        {world.levels.length} level {world.levels.length === 1 ? "chunk" : "chunks"}
      </div>
    </WkPanel>
  );
}

export function WkSaves() {
  const { serverID } = useParams();
  const id = Number(serverID);
  const { isAdmin, can } = useAuth();
  const queryClient = useQueryClient();
  // Asked, not assumed: an on-demand save exists only where the dwbridge
  // mod is running. When it isn't, its reason replaces the note that would
  // otherwise promise something this server can't do.
  const saveCmd = useCommand(id, "save", can("save"));
  const saveBlocked = saveCmd.known && !saveCmd.supported;

  const backupsQuery = useQuery({
    queryKey: ["backups", id],
    queryFn: () => api.listBackups(id),
    enabled: isAdmin,
    refetchInterval: 30_000,
  });
  // The game autosaves every ~5 minutes; 30s keeps "last written" honest
  // without outpacing the parse cache behind the endpoint.
  const worldQuery = useQuery({
    queryKey: ["world", id],
    queryFn: () => api.getWorld(id),
    enabled: isAdmin,
    refetchInterval: 30_000,
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["backups", id] });
  const run = useMutation({
    mutationFn: () => api.runBackup(id),
    onSuccess: () => {
      toast.success("Snapshot started");
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message || "Snapshot failed to start"),
  });
  const remove = useMutation({
    mutationFn: (name: string) => api.deleteBackup(id, name),
    onSuccess: () => {
      toast.success("Snapshot deleted");
      invalidate();
    },
    onError: (e: Error) => toast.error(e.message || "Delete failed"),
  });
  // On-demand save through the dwbridge mod. The world panel is the
  // confirmation: "Last written" ticks to just now and the save revision
  // counts up, so refetch it. A server with no bridge answers 501 with its
  // own reason, which the toast relays.
  const saveWorld = useMutation({
    mutationFn: () => api.save(id),
    onSuccess: () => {
      toast.success("World saved");
      queryClient.invalidateQueries({ queryKey: ["world", id] });
    },
    onError: (e) => toast.error("Save failed", { description: errorDetail(e) ?? (e as Error).message }),
  });

  if (!isAdmin) {
    return (
      <div className="wildskeeper min-h-full font-wkbody">
        <div className="mx-auto max-w-[1180px] p-4 lg:p-7">
          <WkPanel title="World saves">
            <p className="text-sm text-wk-mist">
              Save snapshots hold the whole world, so only stewards with the admin role can see them.
            </p>
          </WkPanel>
        </div>
      </div>
    );
  }

  const backups = backupsQuery.data;

  const world = worldQuery.data?.available ? worldQuery.data.world : undefined;

  return (
    <div className="wildskeeper min-h-full font-wkbody">
      <div className="mx-auto max-w-[1180px] space-y-3.5 p-4 lg:p-7">
        {world && <WorldPanel world={world} />}
        {worldQuery.isError && (
          <WkPanel title="The world">
            <p className="text-sm text-wk-mist">
              The save is there but could not be read — {(worldQuery.error as Error).message}
            </p>
          </WkPanel>
        )}
        <WkPanel
          title="World saves"
          meta={
            backups
              ? `${backups.snapshots.length} kept · ${sizeLabel(backups.totalBytes)}${
                  backups.intervalHours ? ` · every ${backups.intervalHours}h` : " · no schedule"
                }`
              : undefined
          }
        >
          {backupsQuery.isLoading && <p className="text-sm text-wk-mist">Reading the vault…</p>}
          {backups && !backups.available && (
            <p className="text-sm text-wk-mist">
              This server has no save path configured, so there is nothing to snapshot. Set one in Settings.
            </p>
          )}
          {backups?.available && (
            <>
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <button
                  onClick={() => run.mutate()}
                  disabled={run.isPending || backups.running}
                  className="rounded border border-wk-brass bg-gradient-to-b from-[#2a2416] to-[#1e1a10] px-4 py-1.5 font-bold tracking-[0.05em] text-wk-brasshi transition hover:brightness-125 disabled:opacity-50"
                >
                  {backups.running ? "Snapshot in progress…" : "Take snapshot now"}
                </button>
                {can("save") && (
                  <button
                    onClick={() => saveWorld.mutate()}
                    disabled={saveWorld.isPending || saveBlocked}
                    title="Ask the game to write the world to disk now — snapshots read that file"
                    className="rounded border border-wk-edge px-4 py-1.5 text-wk-mist transition hover:border-wk-brass hover:text-wk-brasshi disabled:opacity-50"
                  >
                    {saveWorld.isPending ? "Saving…" : "Save world"}
                  </button>
                )}
                <span className="text-xs text-wk-mist">
                  {saveBlocked
                    ? saveCmd.reason
                    : "The game autosaves every ~5 minutes — Save world writes the live world first, so a snapshot is up to the second."}
                </span>
              </div>
              {backups.snapshots.length === 0 ? (
                <p className="text-sm text-wk-mist">The vault is empty — take the first snapshot.</p>
              ) : (
                <div>
                  {backups.snapshots.map((s) => (
                    <div
                      key={s.name}
                      className="flex items-center justify-between gap-2.5 border-t border-wk-edge py-2.5 first:border-t-0 first:pt-0"
                    >
                      <div>
                        <span className="font-mono text-xs text-wk-parchment">{s.name}</span>
                        <br />
                        <span className="text-xs text-wk-mist">
                          {new Date(s.ts).toLocaleString()} · {sizeLabel(s.bytes)}
                        </span>
                      </div>
                      <div className="flex gap-1.5">
                        <a
                          href={api.backupDownloadURL(id, s.name)}
                          className="rounded-sm border border-wk-edge px-2.5 py-0.5 text-xs text-wk-mist transition hover:border-wk-brass hover:text-wk-brasshi"
                        >
                          Download
                        </a>
                        <button
                          onClick={() => {
                            if (confirm(`Delete snapshot ${s.name}? This cannot be undone.`)) remove.mutate(s.name);
                          }}
                          className="rounded-sm border border-wk-edge px-2.5 py-0.5 text-xs text-wk-mist transition hover:border-wk-emberdim hover:text-wk-ember"
                        >
                          Delete
                        </button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
          <WkNote>
            The live server loads the newest .sav in SaveGames/ on start, and the filename must match the world name.
            Restoring a snapshot in place lands with the agent's supervisor work — until then, download and place it
            by hand while the server is stopped.
          </WkNote>
        </WkPanel>
      </div>
    </div>
  );
}
