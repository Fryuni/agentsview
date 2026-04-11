const DEBOUNCE_MS = 5_000;
const TIMEOUT_MS = 3_000;

/**
 * Set up a visibilitychange listener that pings the backend when the
 * page becomes visible. If the backend is unreachable (network error,
 * timeout, non-OK status), the page reloads automatically.
 *
 * This recovers from stale WebView connections after macOS sleep/wake
 * cycles or extended background periods in the Tauri desktop app.
 *
 * Returns a cleanup function that removes the listener.
 */
export function setupVisibilityHealthCheck(
  baseUrl: string,
): () => void {
  let lastCheck = 0;

  function onVisibilityChange() {
    if (document.visibilityState !== "visible") return;
    const now = Date.now();
    if (now - lastCheck < DEBOUNCE_MS) return;
    lastCheck = now;

    fetch(`${baseUrl}/version`, {
      signal: AbortSignal.timeout(TIMEOUT_MS),
    })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
      })
      .catch(() => {
        window.location.reload();
      });
  }

  document.addEventListener("visibilitychange", onVisibilityChange);
  return () =>
    document.removeEventListener(
      "visibilitychange",
      onVisibilityChange,
    );
}
