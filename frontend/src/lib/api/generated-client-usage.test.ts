import { describe, expect, it } from "vitest";
import source from "./client.ts?raw";

describe("API client implementation", () => {
  it("routes JSON API calls through generated OpenAPI services", () => {
    expect(source).toContain("from \"./generated/index\"");
    expect(source).toContain("SessionsService.getApiV1Sessions");
    expect(source).toContain("AnalyticsService.getApiV1AnalyticsSummary");
    expect(source).not.toContain("function fetchJSON");
    expect(source).not.toContain("function buildQuery");
  });
});
