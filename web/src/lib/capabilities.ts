import { useQuery } from "@tanstack/react-query";
import { api, type CommandCapability } from "./api";

/**
 * What this server's commands can do, asked once and shared.
 *
 * Capability moves with the dwbridge mod on the server, not with anything
 * the console does, so this is deliberately not polled — a stale answer for
 * a minute is fine, and the command itself is still the authority when it
 * runs.
 */
export function useCapabilities(serverId: number, enabled = true) {
  return useQuery({
    queryKey: ["capabilities", serverId],
    queryFn: () => api.serverCapabilities(serverId),
    enabled,
    retry: false,
    staleTime: 60_000,
  });
}

/** A command's availability, as much as the console currently knows. */
export interface CommandState extends CommandCapability {
  /** False while the probe is in flight, when it failed, or for a game that
   * can't be probed. Callers must stay optimistic then — hiding a control
   * because the answer hasn't arrived is worse than offering one that turns
   * out to be unavailable, which explains itself when clicked. */
  known: boolean;
}

/**
 * Whether `op` can be served on this server. Unknown reads as supported on
 * purpose: every caller assumed exactly that before probing existed, and a
 * refused command still returns its own explanation.
 */
export function useCommand(serverId: number, op: string, enabled = true): CommandState {
  const { data } = useCapabilities(serverId, enabled);
  const entry = data?.commands?.[op];
  if (!data?.probed || !entry) return { supported: true, known: false };
  return { ...entry, known: true };
}
