import test from "node:test";
import assert from "node:assert/strict";
import plugin from "./index.js";

const secret = "fixture-access-never-export", refresh = "fixture-refresh-never-export";
const connection = { type: "credential", id: "cred_fixture", method: "oauth" };
const credential = { type: "oauth", access: secret, refresh, metadata: { orgID: "org_fixture" } };
const org = { org: { id: "org_fixture" }, userId: "user_fixture", email: "private@example.invalid" };
const quota = { subscriberUserId: "user_fixture", product: "go", access: { meters: {
  fiveHour: { usedMicroCents: "25", limitMicroCents: "100", resetsAt: null },
  week: { usedMicroCents: "10", limitMicroCents: "100", resetsAt: "2030-01-07T00:00:00Z" },
  month: { usedMicroCents: "5", limitMicroCents: "100", resetsAt: "2030-02-01T00:00:00Z" }
} } };
async function run(t, options = {}) {
  let handlers, activeCalls = 0, resolves = 0;
  const requests = [];
  const ctx = { app: { version: options.version ?? "2.1.4" }, rpc: { register: async (definition, implementation) => {
    assert.equal(definition.id, "wi.quota");
    // OpenCode v2.1.4 rejects JSON Schema patterns during RPC validation.
    assert.ok(!JSON.stringify(definition).includes('"pattern"'));
    handlers = implementation;
  } }, integration: { connection: {
    active: async () => options.active ? options.active(++activeCalls) : structuredClone(connection),
    resolve: async () => options.resolve ? options.resolve(++resolves) : structuredClone(credential)
  } } };
  await plugin.setup(ctx);
  t.mock.method(globalThis, "fetch", async (url, init) => {
    requests.push({ url, init });
    assert.equal(init.method, "GET"); assert.equal(init.redirect, "error");
    assert.equal(init.headers.authorization, `Bearer ${secret}`);
    assert.equal(init.headers["x-org-id"], "org_fixture");
    assert.ok(init.signal instanceof AbortSignal);
    if (options.fetch) return options.fetch(url, init);
    return Response.json(url.endsWith("/orgs/current") ? (options.org ?? org) :
      (Object.hasOwn(options, "quota") ? options.quota : quota));
  });
  const identity = await handlers.identity({});
  assert.equal(requests.length, 0, "identity must not query backend quota");
  const output = identity.state !== "ok" ? identity : await handlers.read({ binding: options.binding ?? identity.binding }, { signal: new AbortController().signal });
  const encoded = JSON.stringify({ identity, output });
  for (const value of [secret, refresh, "private@example.invalid", "org_fixture", "cred_fixture", "user_fixture"]) assert.ok(!encoded.includes(value), value);
  return { identity, output, requests, handlers };
}
test("same-account OAuth bridge returns normalized remaining quota only", async t => {
  const { identity, output, requests } = await run(t);
  assert.match(identity.binding, /^[0-9a-f]{64}$/);
  assert.equal(output.state, "ok"); assert.equal(output.binding, identity.binding);
  assert.deepEqual(output.windows.map(w => w.remaining_percent), [75,90,95]);
  assert.equal(output.windows[0].resets_at, null);
  assert.deepEqual(output.windows.map(w => w.duration_seconds), [18000,604800,null]);
  assert.deepEqual(requests.map(r => r.url), ["https://opencode.ai/console/api/orgs/current","https://opencode.ai/console/api/go/status"]);
});
test("missing connection never resolves or queries", async t => {
  const { output, requests } = await run(t, { active: () => undefined, resolve: () => { throw Error("must not resolve"); } });
  assert.equal(output.reason, "no_login"); assert.equal(requests.length, 0);
});
test("native v1 and non-OAuth authentication are unsupported", async t => {
  assert.equal((await run(t, { version: "1.0" })).output.state, "unsupported");
  assert.equal((await run(t, { resolve: () => ({ type: "key", key: secret }) })).output.reason, "oauth_required");
});
test("untrusted origins and missing workspace fail before HTTP", async t => {
  const result = await run(t, { resolve: () => ({ ...credential, metadata: { ...credential.metadata, server: "https://untrusted.invalid" } }) });
  assert.equal(result.output.reason, "console_origin"); assert.equal(result.requests.length, 0);
  assert.equal((await run(t, { resolve: () => ({ ...credential, metadata: {} }) })).output.reason, "identity_missing");
});
test("workspace mismatch blocks quota read", async t => {
  const result = await run(t, { org: { ...org, org: { id: "org_other" } } });
  assert.equal(result.output.reason, "workspace_mismatch"); assert.equal(result.requests.length, 1);
});
test("owner mismatch and paused/absent entitlement never return cached-looking quota", async t => {
  for (const [q, reason] of [[{ ...quota, subscriberUserId: "other" },"owner_mismatch"],[null,"no_subscription"],[{ ...quota, access: null },"access_paused"]]) {
    const { output } = await run(t, { quota: q });
    assert.equal(output.reason, reason); assert.equal(output.windows, undefined);
  }
});
test("binding change before read prevents HTTP", async t => {
  const result = await run(t, { active: count => ({ ...connection, id: count === 1 ? "cred_fixture" : "cred_other" }) });
  assert.equal(result.output.reason, "account_changed"); assert.equal(result.requests.length, 0);
});
test("connection switch during fetch discards data", async t => {
  const result = await run(t, { active: count => ({ ...connection, id: count < 3 ? "cred_fixture" : "cred_other" }) });
  assert.equal(result.output.reason, "account_changed"); assert.equal(result.output.windows, undefined);
});
test("workspace switch on same credential discards data", async t => {
  const result = await run(t, { resolve: count => ({ ...credential, metadata: { orgID: count < 3 ? "org_fixture" : "org_other" } }) });
  assert.equal(result.output.reason, "account_changed"); assert.equal(result.output.windows, undefined);
});
for (const status of [401,403,429,503]) test(`HTTP ${status} returns a fixed error, never raw body`, async t => {
  const result = await run(t, { fetch: () => new Response(secret, { status, headers: { "retry-after": "900" } }) });
  assert.equal(result.output.state, status === 401 ? "expired_auth" : "unavailable");
  assert.equal(result.output.binding, result.identity.binding);
  if (status === 429) assert.equal(result.output.retry_after_seconds, 900);
  assert.equal(result.output.windows, undefined);
});
test("network exceptions and oversized/malformed bodies do not escape", async t => {
  for (const fetch of [() => { throw Error(secret); },() => new Response("x".repeat(262145)),() => new Response(secret)]) {
    const { output } = await run(t, { fetch });
    assert.equal(output.state, "unavailable"); assert.equal(output.windows, undefined);
  }
});
test("invalid meter values and missing resets fail closed", async t => {
  for (const patch of [{ limitMicroCents: "0" },{ usedMicroCents: "-1" },{ usedMicroCents: "NaN" },{ resetsAt: "changed" },{ resetsAt: null }]) {
    const q = structuredClone(quota); Object.assign(q.access.meters.week, patch);
    const { output } = await run(t, { quota: q });
    assert.equal(output.reason, "schema_changed"); assert.equal(output.windows, undefined);
  }
});
test("exhaustion clamps to zero without estimating absolute capacity", async t => {
  const q = structuredClone(quota); q.access.meters.week.usedMicroCents = "150";
  const result = await run(t, { quota: q });
  assert.equal(result.output.windows[1].remaining_percent, 0);
  assert.ok(!JSON.stringify(result.output).includes("MicroCents"));
});
test("tiny nonzero remainders/spend never become exhausted or full", async t => {
  for (const used of ["1", "9999999999999999999999999999999999999999"]) {
    const q = structuredClone(quota);
    q.access.meters.week.limitMicroCents = "10000000000000000000000000000000000000000";
    q.access.meters.week.usedMicroCents = used;
    const result = await run(t, { quota: q });
    const percent = result.output.windows[1].remaining_percent;
    assert.ok(percent > 0 && percent < 100);
  }
});
test("invalid binding cannot become an arbitrary proxy request", async t => {
  const result = await run(t, { binding: "https://untrusted.invalid" });
  assert.equal(result.output.reason, "identity_missing"); assert.equal(result.requests.length, 0);
});
