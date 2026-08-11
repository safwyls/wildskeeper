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

describe("ServerPower launch mode", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    toastSuccess.mockClear();
    toastError.mockClear();
    vi.spyOn(api, "containerStatus").mockResolvedValue(runningState);
    vi.spyOn(api, "steamUpdateStatus").mockResolvedValue({
      job: null,
      agent: { version: "test", apiVersion: 1, mode: "supervisor", installDirOk: true, diskFreeBytes: 0 },
    });
  });

  const nativeLaunch = {
    profile: "native",
    label: "Native Linux build",
    mods: false,
    installed: true,
    runnable: true,
    available: ["native", "wine"],
    pendingRestart: false,
    configPath: "RSDragonwilds/Saved/Config/LinuxServer/DedicatedServer.ini",
  };

  function renderWithAgent() {
    return renderWithProviders(<ServerPower serverId={1} agentUrl="http://agent:8811" />);
  }

  it("confirms before switching build, naming the re-download it costs", async () => {
    vi.spyOn(api, "serverLaunch").mockResolvedValue(nativeLaunch);
    const set = vi.spyOn(api, "setServerLaunch").mockResolvedValue({
      ...nativeLaunch,
      profile: "wine",
      mods: true,
      installed: false,
    });
    renderWithAgent();

    await userEvent.click(await screen.findByRole("button", { name: "Windows + mods" }));

    // Switching depots is not a restart, and the dialog has to say so before
    // the click, not after.
    expect(await screen.findByText(/different Steam depots/i)).toBeInTheDocument();
    expect(set).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Use this build" }));
    await waitFor(() => expect(set).toHaveBeenCalledWith(1, "wine"));
    // The new build isn't downloaded, so the toast points at the next step
    // rather than telling them to restart into something that isn't there.
    expect(toastSuccess).toHaveBeenCalledWith(
      expect.stringContaining("Windows + mods"),
      expect.objectContaining({ description: expect.stringContaining("Update server") }),
    );
  });

  it("marks the active build and does not re-select it", async () => {
    vi.spyOn(api, "serverLaunch").mockResolvedValue(nativeLaunch);
    const set = vi.spyOn(api, "setServerLaunch").mockResolvedValue(nativeLaunch);
    renderWithAgent();

    const active = await screen.findByRole("button", { name: "Native Linux" });
    expect(active).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(active);
    expect(set).not.toHaveBeenCalled();
    expect(screen.queryByText(/different Steam depots/i)).not.toBeInTheDocument();
  });

  it("says a switch is waiting on a restart", async () => {
    vi.spyOn(api, "serverLaunch").mockResolvedValue({
      ...nativeLaunch,
      profile: "wine",
      mods: true,
      pendingRestart: true,
    });
    renderWithAgent();

    expect(await screen.findByText(/restart to switch to Windows \+ mods/i)).toBeInTheDocument();
  });

  it("offers to rebuild the agent when its image cannot run the chosen build", async () => {
    // The Wine profile selected on an agent image with no Wine in it. This
    // is the state a TrueNAS operator lands in: the container was
    // provisioned from the plain image and nothing in their apps view can
    // change it, so the console has to offer the rebuild itself.
    vi.spyOn(api, "serverLaunch").mockResolvedValue({
      ...nativeLaunch,
      profile: "wine",
      mods: true,
      runnable: false,
    });
    const rebuild = vi.spyOn(api, "recreateAgent").mockResolvedValue({
      container: "wkagent-ashenfall",
      image: "ghcr.io/safwyls/wkagent:latest-wine",
      previousImage: "ghcr.io/safwyls/wkagent:latest",
    });
    renderWithAgent();

    expect(await screen.findByText(/has no Wine in it/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Rebuild agent on the Wine image/i }));

    // Removing and recreating a container is worth confirming, and the
    // dialog has to say the world survives it.
    expect(await screen.findByText(/not touched/i)).toBeInTheDocument();
    expect(rebuild).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Rebuild agent" }));
    await waitFor(() => expect(rebuild).toHaveBeenCalledWith(1, "latest-wine"));
    expect(toastSuccess).toHaveBeenCalledWith("Agent rebuilt", expect.anything());
  });

  it("does not offer a rebuild when the image can already run the build", async () => {
    vi.spyOn(api, "serverLaunch").mockResolvedValue({ ...nativeLaunch, profile: "wine", mods: true, runnable: true });
    renderWithAgent();

    await screen.findByText("Launch mode");
    expect(screen.queryByRole("button", { name: /Rebuild agent/i })).not.toBeInTheDocument();
  });

  it("shows no launch row for an agent that doesn't run the game", async () => {
    // Companion mode answers 400 — there is no build to choose, so the
    // control should be absent rather than broken.
    vi.spyOn(api, "serverLaunch").mockRejectedValue(new ApiError(400, "this agent does not run the game"));
    renderWithAgent();

    await screen.findByRole("button", { name: "Stop" });
    expect(screen.queryByText("Launch mode")).not.toBeInTheDocument();
  });
});

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

  it("disables Save world, with the reason, when the server has no way to save", async () => {
    const reason = "an on-demand save needs the dwbridge mod";
    vi.spyOn(api, "serverCapabilities").mockResolvedValue({
      probed: true,
      commands: { save: { supported: false, reason } },
    });
    const save = vi.spyOn(api, "save").mockResolvedValue(undefined);
    renderPower();

    const button = await screen.findByRole("button", { name: "Save world" });
    await waitFor(() => expect(button).toBeDisabled());
    expect(button).toHaveAttribute("title", reason);

    // And the stop dialog stops offering a save-first path that could only
    // ever fail.
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    await screen.findByText(/Stop the server\?/i);
    expect(screen.queryByRole("button", { name: /Save world, then/ })).not.toBeInTheDocument();
    expect(save).not.toHaveBeenCalled();
  });

  it("stays optimistic while the capability answer is unknown", async () => {
    // A game that can't be probed, which is what every game was before the
    // probe existed. Hiding the control would be the wrong call: the command
    // explains itself if it turns out to be unavailable.
    vi.spyOn(api, "serverCapabilities").mockResolvedValue({ probed: false, commands: {} });
    const save = vi.spyOn(api, "save").mockResolvedValue(undefined);
    renderPower();

    await userEvent.click(await screen.findByRole("button", { name: "Save world" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith(1));
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
