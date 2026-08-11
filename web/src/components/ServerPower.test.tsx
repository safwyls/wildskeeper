import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServerPower } from "./ServerPower";
import { api, ApiError } from "../lib/api";
import { renderWithProviders } from "../test/utils";

// The component reads only `can` from auth; grant everything so the save
// and power controls both render.
vi.mock("../lib/auth", () => ({
  useAuth: () => ({
    username: "admin",
    isAdmin: true,
    can: () => true,
    logout: vi.fn(),
  }),
}));

const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}));

const runningState = {
  name: "wkagent-main",
  status: "running",
  running: true,
  startedAt: "2026-08-11T00:00:00Z",
  exitCode: 0,
};

function renderPower() {
  return renderWithProviders(<ServerPower serverId={1} />);
}

describe("ServerPower on-demand save", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    toastSuccess.mockClear();
    toastError.mockClear();
    vi.spyOn(api, "containerStatus").mockResolvedValue(runningState);
  });

  it("saves the world from the power row", async () => {
    const save = vi.spyOn(api, "save").mockResolvedValue(undefined);
    renderPower();

    await userEvent.click(await screen.findByRole("button", { name: "Save world" }));

    await waitFor(() => expect(save).toHaveBeenCalledWith(1));
    expect(toastSuccess).toHaveBeenCalledWith("World saved");
  });

  it("relays the game's own reason when the save is refused", async () => {
    const reason = "an on-demand save needs the dwbridge mod";
    vi.spyOn(api, "save").mockRejectedValue(new ApiError(501, reason));
    renderPower();

    await userEvent.click(await screen.findByRole("button", { name: "Save world" }));

    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith("Save failed", { description: reason }),
    );
  });

  it("offers save-then-stop in the stop dialog, stopping only after the save lands", async () => {
    const save = vi.spyOn(api, "save").mockResolvedValue(undefined);
    const act = vi.spyOn(api, "containerAction").mockResolvedValue(runningState);
    renderPower();

    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    await userEvent.click(await screen.findByRole("button", { name: "Save world, then stop" }));

    await waitFor(() => expect(act).toHaveBeenCalledWith(1, "stop"));
    expect(save).toHaveBeenCalledWith(1);
    // The save must land before the stop is even asked for.
    expect(save.mock.invocationCallOrder[0]).toBeLessThan(act.mock.invocationCallOrder[0]);
  });

  it("does not stop when the pre-stop save fails", async () => {
    vi.spyOn(api, "save").mockRejectedValue(new ApiError(502, "agent unreachable"));
    const act = vi.spyOn(api, "containerAction").mockResolvedValue(runningState);
    renderPower();

    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    await userEvent.click(await screen.findByRole("button", { name: "Save world, then stop" }));

    await waitFor(() => expect(toastError).toHaveBeenCalled());
    expect(act).not.toHaveBeenCalled();
    // The dialog stays open so plain Stop is still available.
    expect(screen.getByRole("button", { name: "Save world, then stop" })).toBeInTheDocument();
  });

  it("keeps the save control for agent-managed servers whose docker power is off", async () => {
    vi.spyOn(api, "containerStatus").mockRejectedValue(
      new ApiError(400, "docker power control not configured"),
    );
    vi.spyOn(api, "steamUpdateStatus").mockResolvedValue({
      job: null,
      agent: { version: "test", apiVersion: 1, mode: "supervisor", installDirOk: true, diskFreeBytes: 0 },
    });
    renderWithProviders(<ServerPower serverId={1} agentUrl="http://agent:8811" />);

    expect(await screen.findByRole("button", { name: "Save world" })).toBeInTheDocument();
    // The docker power buttons stay hidden — save is the only lever here.
    expect(screen.queryByRole("button", { name: "Stop" })).not.toBeInTheDocument();
  });
});
