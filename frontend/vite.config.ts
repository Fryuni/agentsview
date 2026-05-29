import { execSync } from "node:child_process";
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

function gitCommit(): string {
  try {
    return execSync("git rev-parse --short HEAD", {
      encoding: "utf-8",
    }).trim();
  } catch {
    return "unknown";
  }
}

const apiTarget = process.env.VITE_API_TARGET ?? "http://127.0.0.1:8080";
const apiTargetOrigin = new URL(apiTarget).origin;

function requestOriginMatchesDevServer(
  origin: string | undefined,
  host: string | undefined,
): boolean {
  if (!origin || !host) return false;
  return origin === `http://${host}` || origin === `https://${host}`;
}

export default defineConfig({
  base: "/",
  plugins: [svelte()],
  define: {
    "import.meta.env.VITE_BUILD_COMMIT": JSON.stringify(
      gitCommit(),
    ),
  },
  resolve: {
    conditions: ["browser"],
  },
  server: {
    proxy: {
      "/api": {
        target: apiTarget,
        changeOrigin: true,
        configure(proxy) {
          proxy.on("proxyReq", (proxyReq, req) => {
            const origin = req.headers.origin;
            if (
              requestOriginMatchesDevServer(
                typeof origin === "string" ? origin : undefined,
                req.headers.host,
              )
            ) {
              proxyReq.setHeader("Origin", apiTargetOrigin);
            }
          });
        },
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    exclude: ["e2e/**", "node_modules/**"],
    server: {
      deps: {
        inline: ["svelte"],
      },
    },
  },
});
