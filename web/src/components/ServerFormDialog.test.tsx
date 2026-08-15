import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServerFormDialog } from "./ServerFormDialog";
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

function discovered(name: string, mode: string): DiscoveredServer {
  return { name, image: "ghcr.io/safwyls/wkagent:latest", mode, running: true, agentPort: 8821, registered: false };
}

function openCreate() {
  return renderWithProviders(<ServerFormDialog open onOpenChange={() => {}} mode="create" />);
}

describe("ServerFormDialog adoption candidates", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    toastSuccess.mockClear();
    toastError.mockClear();
    vi.spyOn(api, "provisionDefaults").mockResolvedValue({ available: true, host: "192.168.1.9" });
  });

  // The regression that hid adoption behind Ilmari: the legacy provisioner
  // reports mode "supervisor" for game servers, but Ilmari's discovery
  // cannot read container env, so its candidates arrive with mode "".
  // Both must be offered; only a known provisioner is withheld.
  it("offers legacy and Ilmari-shaped candidates, but never a known provisioner", async () => {
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({
      available: true,
      servers: [
        discovered("wkagent-legacy", "supervisor"),
        discovered("wkagent-via-ilmari", ""),
        discovered("wkprovisioner", "provisioner"),
      ],
    });
    openCreate();

    expect(await screen.findByText("wkagent-legacy")).toBeInTheDocument();
    expect(screen.getByText("wkagent-via-ilmari")).toBeInTheDocument();
    expect(screen.queryByText("wkprovisioner")).not.toBeInTheDocument();
  });

  it("adopts a candidate with one click", async () => {
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({
      available: true,
      servers: [discovered("wkagent-ashenfall", "")],
    });
    const adopt = vi.spyOn(api, "adoptServer").mockResolvedValue({
      server: makeServer({ name: "Ashenfall" }),
    });
    openCreate();

    await userEvent.click(await screen.findByText("wkagent-ashenfall"));

    await waitFor(() => expect(adopt).toHaveBeenCalledWith("wkagent-ashenfall", "192.168.1.9"));
    expect(toastSuccess).toHaveBeenCalledWith('Adopted "Ashenfall"');
  });

  it("shows no adoption section when nothing qualifies", async () => {
    vi.spyOn(api, "provisionDiscover").mockResolvedValue({
      available: true,
      servers: [discovered("wkprovisioner", "provisioner")],
    });
    openCreate();

    await screen.findByText("Add an existing server");
    await waitFor(() => expect(api.provisionDiscover).toHaveBeenCalled());
    expect(screen.queryByText(/Found on the provisioner's host/i)).not.toBeInTheDocument();
  });
});
