import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { evaluateLocal, QFlag, rolloutBucket, type FeatureFlag } from "./index.js";

const flag: FeatureFlag = {
  flagName: "new-payment-flow",
  projectId: "project_123",
  environment: "prod",
  enabled: true,
  rolloutPercentage: 25,
  targetUsers: ["target-user"],
  version: 3,
  createdAt: new Date(0).toISOString(),
  updatedAt: new Date(0).toISOString(),
};

describe("evaluateLocal", () => {
  it("enables explicitly targeted users", () => {
    assert.equal(evaluateLocal(flag, "target-user").enabled, true);
    assert.equal(evaluateLocal(flag, "target-user").reason, "target_user");
  });

  it("returns a stable rollout bucket", () => {
    assert.equal(rolloutBucket("user-42", "new-payment-flow"), rolloutBucket("user-42", "new-payment-flow"));
  });

  it("does not enable disabled flags unless explicitly targeted", () => {
    const disabled = { ...flag, enabled: false, targetUsers: [] };
    assert.equal(evaluateLocal(disabled, "user-42").enabled, false);
    assert.equal(evaluateLocal(disabled, "user-42").reason, "flag_disabled");
  });

  it("does not evaluate an expired cache entry", async () => {
    let now = 0;
    let calls = 0;
    const client = new QFlag({
      endpoints: "https://flags.example",
      projectId: "project-123",
      environment: "prod",
      cacheTtlMs: 10,
      now: () => now,
      fetchImpl: async () => {
        calls += 1;
        return new Response(JSON.stringify({ flag: flag.flagName, enabled: false, reason: "flag_disabled", version: 4 }));
      },
    });

    client.upsertCachedFlag(flag);
    assert.equal((await client.evaluate(flag.flagName, "user-42")).version, 3);
    now = 11;
    assert.equal((await client.evaluate(flag.flagName, "user-42")).version, 4);
    assert.equal(calls, 1);
  });

  it("sends the configured API token", async () => {
    let authorization = "";
    const client = new QFlag({
      endpoints: "https://flags.example",
      projectId: "project-123",
      environment: "prod",
      apiToken: "secret-token",
      fetchImpl: async (_input, init) => {
        authorization = new Headers(init?.headers).get("Authorization") ?? "";
        return new Response(JSON.stringify([]));
      },
    });

    await client.refresh();
    assert.equal(authorization, "Bearer secret-token");
  });
});
