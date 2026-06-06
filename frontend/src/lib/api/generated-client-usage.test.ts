import { describe, expect, it } from "vitest";
import source from "./client.ts?raw";
import analyticsStoreSource from "../stores/analytics.svelte.ts?raw";
import trendsStoreSource from "../stores/trends.svelte.ts?raw";
import usageStoreSource from "../stores/usage.svelte.ts?raw";

describe("API client implementation", () => {
  it("uses generated OpenAPI services without analytics facades", () => {
    expect(source).toContain("from \"./generated/index\"");
    expect(source).toContain("SessionsService.getApiV1Sessions");
    expect(source).not.toContain("getAnalyticsSummary");
    expect(source).not.toContain("getTrendsTerms");
    expect(source).not.toContain("getUsageSummary");
    expect(analyticsStoreSource).toContain(
      "AnalyticsService.getApiV1AnalyticsSummary",
    );
    expect(trendsStoreSource).toContain(
      "TrendsService.getApiV1TrendsTerms",
    );
    expect(usageStoreSource).toContain(
      "UsageService.getApiV1UsageSummary",
    );
    expect(source).not.toContain("function fetchJSON");
    expect(source).not.toContain("function buildQuery");
  });
});
