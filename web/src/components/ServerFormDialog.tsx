import { useEffect, useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, type Server, type ServerWriteInput } from "../lib/api";
import { cn } from "../lib/utils";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { NumberField } from "./ui/number-field";
import { Label } from "./ui/label";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";

/** How the game is deployed. These are the agent's own `WKAGENT_MODE`
 * values, so the tab a server sits under is the shape it really runs in. */
type Kind = "supervised" | "companion";

const KINDS = [
  ["supervised", "Supervised"],
  ["companion", "Companion"],
] as const;

const KIND_BLURB: Record<Kind, string> = {
  supervised:
    "One wkagent container runs the game itself. Power, updates, saves and settings all flow through the agent — no container name, no path mounts.",
  companion:
    "The game runs in its own container. Power goes through the docker proxy, and files come from a wkagent beside it — or from three paths mounted into Wildskeeper.",
};

const emptyForm: ServerWriteInput = {
  name: "",
  host: "",
  gamePort: 7777,
  joinAddress: "",
  enabled: true,
  savePath: "",
  configPath: "",
  installPath: "",
  agentUrl: "",
  agentToken: "",
  containerName: "",
};

function formStateFor(mode: "create" | "edit", server?: Server): ServerWriteInput {
  if (mode === "edit" && server) {
    return {
      name: server.name,
      host: server.host,
      gamePort: server.gamePort,
      joinAddress: server.joinAddress,
      enabled: server.enabled,
      savePath: server.savePath,
      configPath: server.configPath,
      installPath: server.installPath,
      agentUrl: server.agentUrl,
      agentToken: "",
      containerName: server.containerName,
    };
  }
  return emptyForm;
}

/** Which tab an existing server opens on. A container name or a path mount
 * is what makes a deployment companion-shaped; a provisioned or adopted
 * supervisor row carries an agent URL and nothing else. A bare REST/RCON
 * server opens on Companion too — the game is running somewhere wildskeeper
 * doesn't own, which is exactly what that tab describes. */
function kindFor(mode: "create" | "edit", server?: Server): Kind {
  if (mode === "create" || !server) return "supervised";
  if (server.containerName || server.savePath || server.configPath || server.installPath) return "companion";
  return server.agentUrl ? "supervised" : "companion";
}

/** Fields the selected mode ignores, so a server switched between shapes
 * doesn't keep silently carrying the other one's values. */
const COMPANION_ONLY = [
  ["containerName", "container name"],
  ["savePath", "save path"],
  ["configPath", "config path"],
  ["installPath", "install path"],
] as const;

type Cap = { label: string; on: boolean };

/**
 * What the form as typed will actually switch on. Mirrors the server's own
 * resolution order: saves and settings prefer the agent and fall back to a
 * mount (agentfiles.SavePath / ConfigPath), SteamCMD repair takes the agent
 * or the install path, and power comes from the agent in supervisor mode
 * and from the docker proxy otherwise.
 */
function capabilities(kind: Kind, form: ServerWriteInput, hasToken: boolean): Cap[] {
  const agent = Boolean(form.agentUrl) && (Boolean(form.agentToken) || hasToken);
  if (kind === "supervised") {
    return [
      { label: "power", on: agent },
      { label: "saves", on: agent },
      { label: "settings", on: agent },
      { label: "updates", on: agent },
    ];
  }
  return [
    { label: "power", on: Boolean(form.containerName) },
    { label: "saves", on: agent || Boolean(form.savePath) },
    { label: "settings", on: agent || Boolean(form.configPath) },
    { label: "updates", on: agent || Boolean(form.installPath) },
  ];
}

/** One sentence naming what is still off and what turns it on. */
function readoutHint(kind: Kind, caps: Cap[]): string {
  const off = caps.filter((c) => !c.on).map((c) => c.label);
  if (off.length === 0) {
    return kind === "supervised"
      ? "The agent covers all four — nothing else to set."
      : "Power runs through the docker proxy; files and updates through the agent or the mounts below.";
  }
  if (kind === "supervised") return "Set the agent URL and token below — they turn on all four at once.";

  const parts: string[] = [];
  if (off.includes("power")) parts.push("power needs a container name");
  const files = off.filter((l) => l !== "power");
  if (files.length === 1) parts.push(`${files[0]} needs the agent, or its path mount`);
  else if (files.length > 1) {
    parts.push(`${files.slice(0, -1).join(", ")} and ${files[files.length - 1]} need the agent, or their path mounts`);
  }
  return `Still off — ${parts.join("; ")}.`;
}

export function ServerFormDialog({
  open,
  onOpenChange,
  mode,
  server,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mode: "create" | "edit";
  server?: Server;
}) {
  const queryClient = useQueryClient();
  const [form, setForm] = useState<ServerWriteInput>(() => formStateFor(mode, server));
  // How the game runs decides which fields matter, so the form asks for one
  // shape at a time instead of listing every field a Palworld deployment
  // could possibly have.
  const [kind, setKind] = useState<Kind>(() => kindFor(mode, server));
  // The mount fields sit behind a disclosure so the agent reads as the way
  // in — but a server already running on mounts opens with them showing,
  // since hiding values that exist is how you lose track of them.
  const [mountsOpen, setMountsOpen] = useState(false);

  // Reset to fresh values every time the dialog opens, so stale form state
  // from a previous open (or a different server, in edit mode) doesn't leak in.
  useEffect(() => {
    if (open) {
      setForm(formStateFor(mode, server));
      setKind(kindFor(mode, server));
      setMountsOpen(Boolean(server?.savePath || server?.configPath || server?.installPath));
    }
  }, [open, mode, server]);

  const save = useMutation({
    mutationFn: (input: ServerWriteInput) =>
      mode === "create" ? api.createServer(input) : api.updateServer(server!.id, input),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["servers"] });
      if (mode === "edit") queryClient.invalidateQueries({ queryKey: ["server", result.id] });
      toast.success(mode === "create" ? `Added "${result.name}"` : `Updated "${result.name}"`);
      onOpenChange(false);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to save server"),
  });

  const hasStoredToken = mode === "edit" && Boolean(server?.hasAgentToken);
  const caps = useMemo(() => capabilities(kind, form, hasStoredToken), [kind, form, hasStoredToken]);
  const strays = kind === "supervised" ? COMPANION_ONLY.filter(([key]) => form[key]) : [];

  const agentFields = (
    <div className="grid grid-cols-2 gap-3">
      <div className="space-y-1.5">
        <Label>Agent URL</Label>
        <Input
          value={form.agentUrl}
          placeholder="http://wkagent:8811"
          onChange={(e) => setForm({ ...form, agentUrl: e.target.value })}
        />
      </div>
      <Field
        label="Agent token"
        value={form.agentToken ?? ""}
        onChange={(v) => setForm({ ...form, agentToken: v })}
        type="password"
        placeholder={hasStoredToken ? "unchanged" : undefined}
      />
    </div>
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onClick={(e) => e.stopPropagation()}
        className="flex max-h-[85vh] w-[calc(100vw-2rem)] max-w-xl flex-col overflow-y-auto"
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate(form);
          }}
        >
          <DialogHeader>
            <DialogTitle>{mode === "create" ? "Add an existing server" : `Edit "${server?.name}"`}</DialogTitle>
            <DialogDescription>
              {mode === "create"
                ? "Wildskeeper only needs to know how to reach it — pick the shape it runs in."
                : "Leave the agent token blank to keep the stored one."}
            </DialogDescription>
          </DialogHeader>

          <div className="mt-4">
            <ModeTabs kind={kind} onChange={setKind} />
            <p className="mt-2 text-xs text-muted-foreground">{KIND_BLURB[kind]}</p>
            <WiringReadout caps={caps} hint={readoutHint(kind, caps)} />
          </div>

          <div id={`server-form-${kind}`} role="tabpanel" aria-label={`${kind} settings`}>
            <Section title="Connection">
              <div className="grid grid-cols-2 gap-3">
                <Field label="Name" value={form.name} onChange={(v) => setForm({ ...form, name: v })} />
                <Field label="Host" value={form.host} onChange={(v) => setForm({ ...form, host: v })} />
                <div className="space-y-1.5">
                  <Label>Game port (players)</Label>
                  <NumberField value={form.gamePort} onChange={(v) => setForm({ ...form, gamePort: v })} min={1} />
                </div>
              </div>

              <div className="space-y-1.5">
                <Label>Join address (optional)</Label>
                <Input
                  value={form.joinAddress}
                  onChange={(e) => setForm({ ...form, joinAddress: e.target.value })}
                  placeholder={`${form.host || "play.example.com"}:${form.gamePort}`}
                />
                <p className="text-xs text-muted-foreground">
                  What players type to connect from outside your network. Leave blank to show the host and game port
                  above, which only works on your own network.
                </p>
              </div>
            </Section>

            {kind === "supervised" ? (
              <Section title="Agent">
                {agentFields}
                <p className="text-xs text-muted-foreground">
                  The <code>wkagent</code> container running this game. Token must match the agent's{" "}
                  <code>WKAGENT_TOKEN</code> — the provisioner writes both when it generates the stack.
                </p>

                {strays.length > 0 && (
                  <div className="rounded-xl border border-wk-brasshi/40 bg-wk-brasshi/10 px-3 py-2 text-xs text-wk-parchment/60">
                    <p>
                      This server still has a {joinNames(strays.map(([, name]) => name))} set. A supervised
                      deployment ignores {strays.length === 1 ? "it" : "them"} — the agent owns the files.
                    </p>
                    <button
                      type="button"
                      onClick={() =>
                        setForm({ ...form, containerName: "", savePath: "", configPath: "", installPath: "" })
                      }
                      className="mt-1.5 font-semibold text-wk-ember hover:underline"
                    >
                      Clear {strays.length === 1 ? "it" : "them"}
                    </button>
                  </div>
                )}
              </Section>
            ) : (
              <>
                <Section title="Container">
                  <div className="space-y-1.5">
                    <Label>Container name (optional)</Label>
                    <Input
                      value={form.containerName}
                      placeholder="dragonwilds"
                      onChange={(e) => setForm({ ...form, containerName: e.target.value })}
                    />
                    <p className="text-xs text-muted-foreground">
                      Docker container the game runs in. Turns on start, stop and restart, and needs
                      <code> DOCKER_HOST</code> pointed at a scoped socket proxy.
                    </p>
                  </div>
                </Section>

                <Section title="File access">
                  {agentFields}
                  <p className="text-xs text-muted-foreground">
                    A <code>wkagent</code> sidecar deployed next to the game container. Covers all three paths
                    below at once: saves, the settings editor, backups and SteamCMD repair.
                  </p>

                  <details
                    open={mountsOpen}
                    onToggle={(e) => setMountsOpen(e.currentTarget.open)}
                    className="rounded-xl border border-wk-edge px-3 pb-3"
                  >
                    <summary className="cursor-pointer py-2 text-xs font-semibold text-wk-parchment/50">
                      No agent? Mount the paths instead
                    </summary>
                    <div className="space-y-4">
                      <div className="space-y-1.5">
                        <Label>Save path (optional)</Label>
                        <Input
                          value={form.savePath}
                          placeholder="/saves/myserver"
                          onChange={(e) => setForm({ ...form, savePath: e.target.value })}
                        />
                        <p className="text-xs text-muted-foreground">
                          Container path to the world save folder (<code>SaveGames</code>, holding the
                          <code>.sav</code>), mounted read-only. Turns on the world panel and backups.
                        </p>
                      </div>

                      <div className="space-y-1.5">
                        <Label>Config path (optional)</Label>
                        <Input
                          value={form.configPath}
                          placeholder="/config/myserver"
                          onChange={(e) => setForm({ ...form, configPath: e.target.value })}
                        />
                        <p className="text-xs text-muted-foreground">
                          Container path to the folder holding <code>DedicatedServer.ini</code>, mounted
                          <strong> read-write</strong>. Turns on the settings editor. Keep it separate from the
                          save mount so save data stays read-only.
                        </p>
                      </div>

                      <div className="space-y-1.5">
                        <Label>Install path (optional)</Label>
                        <Input
                          value={form.installPath}
                          placeholder="/dragonwilds"
                          onChange={(e) => setForm({ ...form, installPath: e.target.value })}
                        />
                        <p className="text-xs text-muted-foreground">
                          Container path to the Dragonwilds install root (holds <code>steamapps</code>), mounted
                          <strong> read-write</strong>. Turns on clearing the SteamCMD cache when a game update
                          corrupts it.
                        </p>
                      </div>
                    </div>
                  </details>
                </Section>
              </>
            )}
          </div>


          <DialogFooter className="mt-6">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" className="clip-notch" disabled={save.isPending}>
              {save.isPending ? "Saving..." : mode === "create" ? "Add server" : "Save changes"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The deployment-shape picker. Left/right arrows move between tabs, so it
 * behaves like the tablist it is rather than two adjacent buttons. */
function ModeTabs({ kind, onChange }: { kind: Kind; onChange: (k: Kind) => void }) {
  return (
    <div role="tablist" aria-label="Deployment mode" className="grid grid-cols-2 gap-1 rounded-xl border border-wk-edge bg-wk-parchment/5 p-1">
      {KINDS.map(([value, label]) => (
        <button
          key={value}
          type="button"
          role="tab"
          id={`server-mode-${value}`}
          aria-selected={kind === value}
          // Only one panel is rendered at a time, so the unselected tab has
          // nothing to point at — a dangling aria-controls is worse than none.
          aria-controls={kind === value ? `server-form-${value}` : undefined}
          tabIndex={kind === value ? 0 : -1}
          onClick={() => onChange(value)}
          onKeyDown={(e) => {
            if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
            e.preventDefault();
            const next = KINDS.find(([v]) => v !== kind)![0];
            onChange(next);
            document.getElementById(`server-mode-${next}`)?.focus();
          }}
          className={cn(
            "rounded-lg px-3 py-1.5 font-display text-xs font-bold transition",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-wk-ember/50",
            kind === value ? "bg-wk-panel text-wk-parchment shadow-sm" : "text-wk-parchment/50 hover:text-wk-parchment",
          )}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

/**
 * What the form as typed actually turns on, and what it doesn't. The point
 * of the mode tabs is that a deployment only needs a handful of fields —
 * this is the line that proves it, so you can stop guessing whether a blank
 * box mattered.
 */
function WiringReadout({ caps, hint }: { caps: Cap[]; hint: string }) {
  return (
    <div className="mt-3 rounded-xl border border-wk-edge bg-wk-parchment/5 px-3 py-2">
      <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs">
        {caps.map((c) => (
          <span key={c.label} className={c.on ? "text-wk-ok" : "text-wk-parchment/35"}>
            <span aria-hidden="true">{c.on ? "✓" : "—"}</span>{" "}
            <span className="sr-only">{c.on ? "on:" : "off:"}</span>
            {c.label}
          </span>
        ))}
      </div>
      <p className="mt-1.5 text-xs text-wk-parchment/50">{hint}</p>
    </div>
  );
}

/** A labelled group of fields. The label names what the group switches on,
 * not where the values are stored. */
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-5 space-y-4">
      <p className="text-xs font-semibold uppercase tracking-wide text-wk-parchment/40">{title}</p>
      {children}
    </section>
  );
}

/** "a save path", "a save path and a config path", "a save path, a config
 * path and an install path". */
function joinNames(names: readonly string[]): string {
  if (names.length === 1) return names[0];
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

function Field({
  label,
  value,
  onChange,
  type = "text",
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  placeholder?: string;
}) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      <Input type={type} value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}
