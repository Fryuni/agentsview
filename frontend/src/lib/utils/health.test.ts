import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { setupVisibilityHealthCheck } from "./health.js";

describe("setupVisibilityHealthCheck", () => {
  let originalFetch: typeof globalThis.fetch;
  let reloadSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    originalFetch = globalThis.fetch;
    reloadSpy = vi.fn();
    Object.defineProperty(window, "location", {
      value: { reload: reloadSpy },
      writable: true,
    });
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  function fireVisible() {
    Object.defineProperty(document, "visibilityState", {
      value: "visible",
      configurable: true,
    });
    document.dispatchEvent(new Event("visibilitychange"));
  }

  function fireHidden() {
    Object.defineProperty(document, "visibilityState", {
      value: "hidden",
      configurable: true,
    });
    document.dispatchEvent(new Event("visibilitychange"));
  }

  it("reloads when backend is unreachable", async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new Error("net"));
    const cleanup = setupVisibilityHealthCheck("/api/v1");
    fireVisible();
    await new Promise((r) => setTimeout(r, 50));
    expect(reloadSpy).toHaveBeenCalledOnce();
    cleanup();
  });

  it("does not reload when backend responds OK", async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    const cleanup = setupVisibilityHealthCheck("/api/v1");
    fireVisible();
    await new Promise((r) => setTimeout(r, 50));
    expect(reloadSpy).not.toHaveBeenCalled();
    cleanup();
  });

  it("skips check when document is hidden", async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new Error("net"));
    const cleanup = setupVisibilityHealthCheck("/api/v1");
    fireHidden();
    await new Promise((r) => setTimeout(r, 50));
    expect(globalThis.fetch).not.toHaveBeenCalled();
    cleanup();
  });

  it("debounces rapid visibility changes", async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    const cleanup = setupVisibilityHealthCheck("/api/v1");
    fireVisible();
    fireVisible();
    fireVisible();
    await new Promise((r) => setTimeout(r, 50));
    expect(globalThis.fetch).toHaveBeenCalledOnce();
    cleanup();
  });

  it("reloads on non-OK status", async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValue(new Response("", { status: 502 }));
    const cleanup = setupVisibilityHealthCheck("/api/v1");
    fireVisible();
    await new Promise((r) => setTimeout(r, 50));
    expect(reloadSpy).toHaveBeenCalledOnce();
    cleanup();
  });

  it("removes listener on cleanup", async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new Error("net"));
    const cleanup = setupVisibilityHealthCheck("/api/v1");
    cleanup();
    fireVisible();
    await new Promise((r) => setTimeout(r, 50));
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it("fetches the correct URL", async () => {
    globalThis.fetch = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    const cleanup = setupVisibilityHealthCheck("/custom/base");
    fireVisible();
    await new Promise((r) => setTimeout(r, 50));
    expect(globalThis.fetch).toHaveBeenCalledWith(
      "/custom/base/version",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    cleanup();
  });
});
