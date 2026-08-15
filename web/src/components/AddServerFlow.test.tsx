import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AddServerFlow } from "./AddServerFlow";
import { api, type DiscoveredServer } from "../lib/api";
import { makeServer, renderWithProviders } from "../test/utils";

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}));

function discovered(name: string, mode: string, registered = false): DiscoveredServer {
  return { name, image: "ghcr.io/safwyls/wkagent:latest", mode, running: true, agentPort: 8821, registered };
}

function openFlow() {
  return renderWithProviders(<AddServerFlow open onOpenChange={() => {}} />);
}

describe("AddServerFlow chooser", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    toastSuccess.mockClear();
    toastError.mockClear();
    vi.spyOn(api, "provisionDefaults").mockResolvedValue({ available: true, host: "192.168.1.9" });
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({ available: true, servers: [] });
  });

  // The regression that hid adoption behind Ilmari: the legacy provisioner
  // reports mode "supervisor" for game servers, but Ilmari's discovery
  // cannot read container env, so its candidates arrive with mode "".
  // Both must be offered; a known provisioner is withheld, and so is a
  // container that's already registered here — it's in the rail already.
  it("offers legacy and Ilmari-shaped candidates, never a provisioner or a registered one", async () => {
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({
      available: true,
      servers: [
        discovered("wkagent-legacy", "supervisor"),
        discovered("wkagent-via-ilmari", ""),
        discovered("wkprovisioner", "provisioner"),
        discovered("wkagent-already-here", "", true),
      ],
    });
    openFlow();

    expect(await screen.findByText("wkagent-legacy")).toBeInTheDocument();
    expect(screen.getByText("wkagent-via-ilmari")).toBeInTheDocument();
    expect(screen.queryByText("wkprovisioner")).not.toBeInTheDocument();
    expect(screen.queryByText("wkagent-already-here")).not.toBeInTheDocument();
    expect(screen.getByText(/2 servers are running on your host/i)).toBeInTheDocument();
  });

  it("adopts a candidate with one click", async () => {
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({
      available: true,
      servers: [discovered("wkagent-ashenfall", "")],
    });
    const adopt = vi.spyOn(api, "adoptServer").mockResolvedValue({
      server: makeServer({ name: "Ashenfall" }),
    });
    openFlow();

    await userEvent.click(await screen.findByText("wkagent-ashenfall"));

    await waitFor(() => expect(adopt).toHaveBeenCalledWith("wkagent-ashenfall", "192.168.1.9"));
    expect(toastSuccess).toHaveBeenCalledWith('Adopted "Ashenfall"');
  });

  it("shows no host report when nothing qualifies", async () => {
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({
      available: true,
      servers: [discovered("wkprovisioner", "provisioner")],
    });
    openFlow();

    await screen.findByText("Add a server");
    await waitFor(() => expect(api.provisionDiscover).toHaveBeenCalled());
    expect(screen.queryByText(/running on your host/i)).not.toBeInTheDocument();
  });

  it("opens the raise wizard from the primary card", async () => {
    openFlow();

    await userEvent.click(await screen.findByText("Raise a new server"));

    expect(await screen.findByText("Raise a server")).toBeInTheDocument();
    expect(screen.queryByText("Add a server")).not.toBeInTheDocument();
  });

  it("opens the manual form from the by-hand card", async () => {
    openFlow();

    await userEvent.click(await screen.findByText("Add an existing server by hand"));

    expect(await screen.findByText("Add an existing server")).toBeInTheDocument();
    expect(screen.queryByText("Add a server")).not.toBeInTheDocument();
  });

  // Without a provisioner the raise path still exists — it generates a
  // stack — but the card must say so instead of promising a deployment.
  it("describes raise honestly when no provisioner is configured", async () => {
    vi.spyOn(api, "provisionDefaults").mockResolvedValue({ available: false });
    openFlow();

    expect(await screen.findByText(/generates a stack to deploy by hand/i)).toBeInTheDocument();
  });
});
