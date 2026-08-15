import { useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { LogOut, Plus, Users as UsersIcon } from "lucide-react";
import { type Server } from "../lib/api";
import { useAuth } from "../lib/auth";
import { cn } from "../lib/utils";
import { WkServerRune } from "./wildskeeper/WkServerRune";
import { AddServerFlow } from "./AddServerFlow";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

/** Desktop icon rail: sigil coin, one rune coin per server, add button, logout. */
export function ServerRail({ servers, activeServerId }: { servers: Server[]; activeServerId: number | null }) {
  const { username, logout, isAdmin } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [addOpen, setAddOpen] = useState(false);

  const goToServer = (id: number) => navigate(`/servers/${id}`);

  return (
    <aside className="flex w-[72px] shrink-0 flex-col items-center gap-3 border-r border-black/20 bg-wk-ink py-4">
      <div className="mb-2 flex h-9 w-9 items-center justify-center rounded-full border border-wk-brass bg-wk-panel font-wkdisplay text-sm font-bold text-wk-brasshi" title="Wildskeeper">W</div>

      {servers.map((server) => (
        <WkServerRune
          key={server.id}
          server={server}
          active={server.id === activeServerId}
          onClick={() => goToServer(server.id)}
        />
      ))}

      {/* Creating servers is an admin endpoint; don't offer it to others. */}
      {isAdmin && (
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              onClick={() => setAddOpen(true)}
              className="mt-1 flex h-11 w-11 items-center justify-center rounded-full border-2 border-dashed border-white/20 text-wk-parchment/40 transition hover:border-white/40 hover:text-wk-parchment/70"
            >
              <Plus className="h-5 w-5" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">Add server</TooltipContent>
        </Tooltip>
      )}

      <div className="flex-1" />

      {isAdmin && (
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              onClick={() => navigate("/users")}
              className={cn(
                "flex h-10 w-10 items-center justify-center rounded-full transition",
                location.pathname === "/users"
                  ? "bg-wk-panel text-wk-parchment"
                  : "text-wk-parchment/40 hover:bg-wk-panel hover:text-wk-parchment",
              )}
            >
              <UsersIcon className="h-4 w-4" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="right">Users</TooltipContent>
        </Tooltip>
      )}

      <Tooltip>
        <TooltipTrigger asChild>
          <button
            onClick={() => logout()}
            className="flex h-10 w-10 items-center justify-center rounded-full text-wk-parchment/40 transition hover:bg-wk-panel hover:text-wk-parchment"
          >
            <LogOut className="h-4 w-4" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="right">Log out {username}</TooltipContent>
      </Tooltip>

      <AddServerFlow open={addOpen} onOpenChange={setAddOpen} />
    </aside>
  );
}
