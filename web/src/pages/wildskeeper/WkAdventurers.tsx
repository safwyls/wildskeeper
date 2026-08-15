import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api, type Player } from "../../lib/api";
import { WkNote, WkPanel } from "../../components/wildskeeper/WkPanel";

/**
 * Kick and Ban are rendered, disabled, with the reason — the mock's own
 * pattern. Hiding them would leave stewards wondering where moderation
 * went; a dead button that 502s would be a lie in the other direction.
 * They enable when the dwbridge command tier exists.
 */
const KICK_REASON = "Kicking needs the dwbridge mod — no native console exists";
const BAN_REASON = "Bans are managed in-game via Server Management";

/** A player's position, when the dwbridge telemetry supplied one. The
 * origin doubles as "no data": no legitimate player stands at exact 0,0
 * (open water in this world), and the log-only roster reports zeros. UE
 * units are centimetres — metres read better at a glance. */
function formatPosition(p: Player): string | null {
  if (!p.location_x && !p.location_y) return null;
  return `${Math.round(p.location_x / 100).toLocaleString()}, ${Math.round(p.location_y / 100).toLocaleString()} m`;
}

/** The adventurer table rows, shared by the overview panel and this page. */
export function WkPlayerRows({
  players,
  online,
  loading,
}: {
  serverId: number;
  players: Player[];
  online: boolean;
  loading: boolean;
}) {
  if (loading) return <p className="py-3 text-sm text-wk-mist">Reading the log…</p>;
  if (!online) return <p className="py-3 text-sm text-wk-mist">The server is offline — nobody is in the world.</p>;
  if (players.length === 0)
    return <p className="py-3 text-sm text-wk-mist">The walls stand empty — no adventurers online.</p>;
  return (
    <table className="w-full border-collapse text-sm">
      <thead>
        <tr>
          <th className="px-2.5 pb-2 pt-1 text-left text-[11px] font-medium uppercase tracking-[0.12em] text-wk-mist">
            Name
          </th>
          <th className="px-2.5 pb-2 pt-1 text-right text-[11px] font-medium uppercase tracking-[0.12em] text-wk-mist" />
        </tr>
      </thead>
      <tbody>
        {players.map((p) => (
          <tr key={p.userId}>
            <td className="border-t border-wk-edge px-2.5 py-2.5">
              <span className="mr-2 inline-block h-[7px] w-[7px] rounded-full bg-wk-ok shadow-[0_0_5px_rgba(127,196,106,.6)]" />
              <span className="font-bold text-wk-parchment">{p.name}</span>
              {formatPosition(p) && (
                <span className="ml-2 font-mono text-xs text-wk-mist" title="Live position (dwbridge)">
                  {formatPosition(p)}
                </span>
              )}
            </td>
            <td className="border-t border-wk-edge px-2.5 py-2.5 text-right">
              <button
                disabled
                title={KICK_REASON}
                className="cursor-not-allowed rounded-sm border border-wk-edge px-2.5 py-0.5 text-xs text-wk-mist opacity-40"
              >
                Kick
              </button>
              <button
                disabled
                title={BAN_REASON}
                className="ml-1.5 cursor-not-allowed rounded-sm border border-wk-edge px-2.5 py-0.5 text-xs text-wk-mist opacity-40"
              >
                Ban
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function WkAdventurers() {
  const { serverID } = useParams();
  const id = Number(serverID);

  const infoQuery = useQuery({
    queryKey: ["server-info", id],
    queryFn: () => api.serverInfo(id),
    retry: false,
    refetchInterval: 15_000,
  });
  const playersQuery = useQuery({
    queryKey: ["server-players", id],
    queryFn: () => api.serverPlayers(id),
    refetchInterval: 10_000,
    retry: false,
  });
  const activityQuery = useQuery({
    queryKey: ["server-activity", id, 168],
    queryFn: () => api.serverActivity(id, 168),
  });

  const online = !infoQuery.isError && !!infoQuery.data;

  return (
    <div className="wildskeeper min-h-full font-wkbody">
      <div className="mx-auto max-w-[1180px] space-y-3.5 p-4 lg:p-7">
        <WkPanel
          title="Adventurers"
          meta="from the server log · live positions when the dwbridge mod is running"
          bodyClassName="pt-1.5"
        >
          <WkPlayerRows
            serverId={id}
            players={playersQuery.data ?? []}
            online={online}
            loading={playersQuery.isLoading}
          />
          <WkNote>
            The Owner and Admin roles live in the in-game Server Management menu. Owner may ban and unban anyone,
            offline included; Admins ban online adventurers only and cannot unban.
          </WkNote>
        </WkPanel>

        <WkPanel title="Comings and goings" meta="last 7 days">
          {activityQuery.isLoading && <p className="text-sm text-wk-mist">Loading history…</p>}
          {activityQuery.data &&
            (activityQuery.data.events.length === 0 ? (
              <p className="text-sm text-wk-mist">No joins or leaves recorded this week.</p>
            ) : (
              <ul className="space-y-1.5 text-sm">
                {activityQuery.data.events.slice(0, 40).map((e) => (
                  <li key={e.id} className="flex items-baseline justify-between gap-2 border-t border-wk-edge pt-1.5 first:border-t-0 first:pt-0">
                    <span>
                      <span
                        className={
                          e.event === "join"
                            ? "mr-2 inline-block h-[7px] w-[7px] rounded-full bg-wk-ok"
                            : "mr-2 inline-block h-[7px] w-[7px] rounded-full bg-[#3a4148]"
                        }
                      />
                      <b className="font-bold text-wk-parchment">{e.name}</b>{" "}
                      <span className="text-wk-mist">{e.event === "join" ? "joined the world" : "left the world"}</span>
                    </span>
                    <span className="whitespace-nowrap font-mono text-xs text-wk-mist">
                      {new Date(e.ts).toLocaleString()}
                    </span>
                  </li>
                ))}
              </ul>
            ))}
        </WkPanel>
      </div>
    </div>
  );
}
