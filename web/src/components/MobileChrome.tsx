import { useEffect, useRef, useState } from "react";
import { NavLink, useLocation, useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Gamepad2, LogOut, MoreVertical, Pencil, Plus, Power, RefreshCw, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, errorDetail, type Server } from "../lib/api";
import { useAuth } from "../lib/auth";
import { serverColor, initials } from "../lib/palette";
import { FEATURE_ROUTES, featureLabel } from "../lib/games";
import { canSeeFeature, serverFeatures } from "../lib/visibility";
import { cn, copyText, joinAddressFor } from "../lib/utils";
import { WkServerRune } from "./wildskeeper/WkServerRune";
import { ServerFormDialog } from "./ServerFormDialog";
import { AddServerFlow } from "./AddServerFlow";
import { DeleteServerDialog } from "./DeleteServerDialog";
import { ShutdownDialog } from "./ServerActionDialogs";

// One scrollable row instead of three wrapped ones: nine pages of pills ate
// a third of a phone screen. shrink-0 keeps every pill full-width in the
// scroller; the active pill is auto-centered by the effect in MobileTopBar.
const segmentClass = ({ isActive }: { isActive: boolean }) =>
  cn(
    "shrink-0 whitespace-nowrap rounded-lg px-3 py-1.5 text-center text-sm font-semibold transition",
    isActive ? "bg-wk-ember text-wk-parchment" : "text-wk-parchment/60",
  );

/** Mobile top bar: active server identity, Dashboard/Live map segmented control,
 * and an overflow menu carrying the actions that have no other mobile home. */
export function MobileTopBar({ server }: { server: Server | null }) {
  const { username, logout, can, isAdmin } = useAuth();
  const queryClient = useQueryClient();
  const [menuOpen, setMenuOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [shutdownOpen, setShutdownOpen] = useState(false);

  // Keep the active pill in view as the user navigates — deep pages like
  // Automation live past the right edge of the scroller. Keyed on the
  // server too: on a cold load the pill row only renders once the server
  // query lands, after the pathname effect has already fired.
  const navRef = useRef<HTMLDivElement>(null);
  const { pathname } = useLocation();
  useEffect(() => {
    navRef.current
      ?.querySelector('[aria-current="page"]')
      ?.scrollIntoView({ inline: "center", block: "nearest", behavior: "smooth" });
  }, [pathname, server?.id]);

  const infoQuery = useQuery({
    queryKey: ["server-info", server?.id],
    queryFn: () => api.serverInfo(server!.id),
    retry: false,
    staleTime: 15_000,
    enabled: server !== null,
  });

  const save = useMutation({
    mutationFn: () => api.save(server!.id),
    onSuccess: () => {
      toast.success("World saved");
      // The Saves page's world panel is the visible proof — let it tick.
      queryClient.invalidateQueries({ queryKey: ["world", server!.id] });
    },
    onError: (err) => toast.error("Save failed", { description: errorDetail(err) }),
  });

  const menuItem =
    "flex w-full items-center gap-2.5 px-4 py-2.5 text-left text-sm text-wk-parchment hover:bg-wk-parchment/5 transition-colors";

  return (
    <div className="shrink-0 bg-wk-ink px-4 pb-3 pt-[max(1rem,env(safe-area-inset-top))] text-wk-parchment">
      <div className="flex items-center justify-between">
        {server ? (
          <div className="flex min-w-0 items-center gap-2.5">
            <span
              className="sphere-ring h-9 w-9 shrink-0 rounded-full p-[2px]"
              style={{ "--ring-color": serverColor(server.id) } as React.CSSProperties}
            >
              <span className="flex h-full w-full items-center justify-center rounded-full bg-wk-raise font-display text-xs font-bold">
                {initials(server.name)}
              </span>
            </span>
            <div className="min-w-0">
              <p className="truncate font-display text-sm font-bold leading-tight">{server.name}</p>
              <p className="font-mono text-[11px] text-wk-parchment/50">
                {infoQuery.isSuccess
                  ? `${infoQuery.data.playerCount} online`
                  : infoQuery.isError
                    ? "unreachable"
                    : "checking..."}
              </p>
            </div>
          </div>
        ) : (
          <div className="flex items-center gap-2.5">
            <div className="clip-notch h-8 w-8 rounded-full bg-gradient-to-br from-wk-ember to-wk-brasshi" />
            <p className="font-display text-sm font-bold">Wildskeeper</p>
          </div>
        )}

        <div className="relative">
          <button
            onClick={() => setMenuOpen((o) => !o)}
            className="flex h-8 w-8 items-center justify-center rounded-full bg-wk-panel text-sm text-wk-parchment/70"
          >
            <MoreVertical className="h-4 w-4" />
          </button>
          {menuOpen && (
            <>
              <div className="fixed inset-0 z-20" onClick={() => setMenuOpen(false)} />
              <div className="absolute right-0 top-10 z-30 w-52 overflow-hidden rounded-xl border border-wk-edge bg-wk-bg text-wk-parchment shadow-lg">
                {server && (
                  <>
                    {/* Mobile has no page header, so the dashboard's refresh
                        lives here. */}
                    <button
                      className={menuItem}
                      onClick={() => {
                        setMenuOpen(false);
                        for (const key of [
                          "server-info",
                          "server-players",
                          "server-metrics",
                          "server-metrics-history",
                          "server-settings",
                          "container",
                          "server-pals",
                          "server-guilds",
                          "server-inventory",
                        ]) {
                          queryClient.invalidateQueries({ queryKey: [key, server.id] });
                        }
                      }}
                    >
                      <RefreshCw className="h-4 w-4 text-wk-parchment/50" /> Refresh
                    </button>
                    {/* The join-address chip is desktop-header only, and a
                        phone is where you'd paste it into Discord. */}
                    <button
                      className={menuItem}
                      onClick={async () => {
                        setMenuOpen(false);
                        const address = joinAddressFor(server);
                        if (await copyText(address)) toast.success(`Copied ${address}`);
                      }}
                    >
                      <Gamepad2 className="h-4 w-4 text-wk-parchment/50" /> Copy join address
                    </button>
                    {can("save") && (
                      <button
                        className={menuItem}
                        onClick={() => {
                          setMenuOpen(false);
                          save.mutate();
                        }}
                      >
                        <Save className="h-4 w-4 text-wk-parchment/50" /> Save world
                      </button>
                    )}
                    {can("shutdown") && (
                      <button
                        className={menuItem}
                        onClick={() => {
                          setMenuOpen(false);
                          setShutdownOpen(true);
                        }}
                      >
                        <Power className="h-4 w-4 text-wk-ember" /> Shut down…
                      </button>
                    )}
                    {isAdmin && (
                      <>
                        <button
                          className={menuItem}
                          onClick={() => {
                            setMenuOpen(false);
                            setEditOpen(true);
                          }}
                        >
                          <Pencil className="h-4 w-4 text-wk-parchment/50" /> Edit server…
                        </button>
                        <button
                          className={menuItem}
                          onClick={() => {
                            setMenuOpen(false);
                            setDeleteOpen(true);
                          }}
                        >
                          <Trash2 className="h-4 w-4 text-wk-parchment/50" /> Remove server…
                        </button>
                      </>
                    )}
                    <div className="border-t border-wk-edge" />
                  </>
                )}
                <button
                  className={menuItem}
                  onClick={() => {
                    setMenuOpen(false);
                    logout();
                  }}
                >
                  <LogOut className="h-4 w-4 text-wk-parchment/50" /> Log out {username}
                </button>
              </div>
            </>
          )}
        </div>
      </div>

      {server && (
        <div ref={navRef} className="no-scrollbar scroll-fade-x mt-3 flex gap-0.5 overflow-x-auto rounded-xl bg-wk-panel p-1">
          <NavLink to={`/servers/${server.id}`} end className={segmentClass}>
            Dashboard
          </NavLink>
          {/* Same source of truth as the desktop sidebar: the server's own
              feature list, in nav order, named per game. */}
          {serverFeatures(server).map((feature) =>
            canSeeFeature(server, feature, isAdmin) ? (
              <NavLink key={feature} to={`/servers/${server.id}/${FEATURE_ROUTES[feature]}`} className={segmentClass}>
                {featureLabel(server, feature)}
              </NavLink>
            ) : null,
          )}
          <NavLink to={`/servers/${server.id}/activity`} className={segmentClass}>
            Activity
          </NavLink>
          <NavLink to={`/servers/${server.id}/automation`} className={segmentClass}>
            Automation
          </NavLink>
          {can("settings") && (
            <NavLink to={`/servers/${server.id}/settings`} className={segmentClass}>
              Settings
            </NavLink>
          )}
        </div>
      )}

      {server && (
        <>
          <ShutdownDialog serverId={server.id} open={shutdownOpen} onOpenChange={setShutdownOpen} />
          <ServerFormDialog open={editOpen} onOpenChange={setEditOpen} mode="edit" server={server} />
          <DeleteServerDialog server={server} open={deleteOpen} onOpenChange={setDeleteOpen} />
        </>
      )}
    </div>
  );
}

/** Mobile bottom bar: Pal Sphere per server + add button (admin only —
 * creating servers is an admin endpoint). */
export function MobileBottomRail({ servers, activeServerId }: { servers: Server[]; activeServerId: number | null }) {
  const { isAdmin } = useAuth();
  const navigate = useNavigate();
  const [addOpen, setAddOpen] = useState(false);

  const goToServer = (id: number) => navigate(`/servers/${id}`);

  return (
    <div className="flex shrink-0 items-center justify-around border-t border-black/20 bg-wk-ink pt-2.5 pb-[max(0.625rem,env(safe-area-inset-bottom))]">
      {servers.map((server) => (
        <WkServerRune
          key={server.id}
          server={server}
          active={server.id === activeServerId}
          onClick={() => goToServer(server.id)}
        />
      ))}
      {isAdmin && (
        <>
          <button
            onClick={() => setAddOpen(true)}
            className="flex h-10 w-10 items-center justify-center rounded-full border-2 border-dashed border-white/20 text-wk-parchment/40"
          >
            <Plus className="h-4 w-4" />
          </button>
          <AddServerFlow open={addOpen} onOpenChange={setAddOpen} />
        </>
      )}
    </div>
  );
}
