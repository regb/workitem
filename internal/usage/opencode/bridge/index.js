// Runs inside OpenCode. No credentials, raw responses, or native exceptions
// leave this module. Only fixed, read-only Console metadata requests are allowed.
import { createHash } from "node:crypto";

const consoleURL = "https://opencode.ai/console";
const failure = (reason, extra = {}) => ({ state: "unavailable", reason, ...extra });
const unsupported = (reason) => ({ state: "unsupported", reason });

function serverURL(credential) {
  const raw = credential.metadata?.server ?? consoleURL;
  if (typeof raw !== "string") return null;
  const server = raw.replace(/^https:\/\/console\.opencode\.ai(?=\/|$)/, consoleURL).replace(/\/+$/, "");
  return server === consoleURL ? server : null;
}

async function capture(ctx) {
  if (!/^2\./.test(ctx.app?.version ?? "")) return unsupported("client_version");
  const connection = await ctx.integration.connection.active("opencode");
  if (!connection) return failure("no_login");
  const credential = await ctx.integration.connection.resolve(connection);
  if (!credential || credential.type !== "oauth") return unsupported("oauth_required");
  const server = serverURL(credential);
  if (!server) return unsupported("console_origin");
  const org = credential.metadata?.orgID;
  if (connection.type !== "credential" || typeof connection.id !== "string" ||
      typeof org !== "string" || !/^[a-zA-Z0-9_-]{1,256}$/.test(org) ||
      typeof credential.access !== "string" || !credential.access) return failure("identity_missing");
  const binding = createHash("sha256").update(JSON.stringify([server, connection.id, org])).digest("hex");
  return { state: "ok", binding, server, org, credential };
}

async function finish(ctx, initial, result) {
  const current = await capture(ctx);
  if (current.state !== "ok" || current.binding !== initial.binding) return failure("account_changed");
  return { ...result, binding: initial.binding };
}

function retrySeconds(response) {
  const value = response.headers.get("retry-after");
  if (!value) return undefined;
  const seconds = /^\d+$/.test(value) ? Number(value) : (Date.parse(value) - Date.now()) / 1000;
  return Number.isFinite(seconds) && seconds > 0 ? Math.min(86400, Math.ceil(seconds)) : undefined;
}

async function readJSON(url, headers, signal) {
  const response = await fetch(url, {
    method: "GET", headers, redirect: "error",
    signal: AbortSignal.any([signal, AbortSignal.timeout(8000)])
  });
  if (!response.ok) {
    await response.body?.cancel();
    return { status: response.status, retry: retrySeconds(response) };
  }
  if (!response.body) return { status: response.status, malformed: true };
  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 262144) return { status: response.status, malformed: true };
      chunks.push(value);
    }
  } finally {
    await reader.cancel();
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  try { return { status: response.status, data: JSON.parse(new TextDecoder().decode(bytes)) }; }
  catch { return { status: response.status, malformed: true }; }
}

function integer(value) {
  if (typeof value === "number" && Number.isSafeInteger(value) && value >= 0) return BigInt(value);
  if (typeof value === "string" && /^\d{1,80}$/.test(value)) return BigInt(value);
  return null;
}

function quotaWindows(meters) {
  if (!meters || typeof meters !== "object") return null;
  const result = [];
  for (const [key, id, duration] of [["fiveHour", "five_hour", 18000], ["week", "weekly", 604800], ["month", "monthly", null]]) {
    const meter = meters[key];
    if (!meter || typeof meter !== "object") return null;
    const used = integer(meter.usedMicroCents), limit = integer(meter.limitMicroCents);
    if (used === null || limit === null || limit <= 0n) return null;
    const reset = meter.resetsAt;
    if (reset !== null && (typeof reset !== "string" || !/^\d{4}-\d\d-\d\dT/.test(reset) || !Number.isFinite(Date.parse(reset)))) return null;
    if (reset === null && key !== "fiveHour") return null;
    result.push({
      // Subtract as integers before conversion. Never round a small positive
      // remainder to exhausted, or a small nonzero spend to a full allowance.
      id, remaining_percent: used >= limit ? 0 : used === 0n ? 100
        : Math.min(100 - Number.EPSILON * 100, Number(limit - used) / Number(limit) * 100),
      resets_at: reset, duration_seconds: duration
    });
  }
  return result;
}

function httpFailure(response) {
  if (response.status === 401) return { state: "expired_auth", reason: "authentication_rejected" };
  if (response.status === 429) return failure("rate_limited", { retry_after_seconds: response.retry });
  return failure(response.malformed ? "schema_changed" : "http_error");
}

async function readQuota(ctx, requestedBinding, signal) {
  let initial;
  try {
    initial = await capture(ctx);
    if (initial.state !== "ok") return initial;
    if (initial.binding !== requestedBinding) return failure("account_changed");
    const headers = {
      authorization: `Bearer ${initial.credential.access}`,
      "x-org-id": initial.org,
      "user-agent": `wi-quota/1 (OpenCode ${ctx.app.version} plugin)`,
      accept: "application/json"
    };
    let result;
    try {
      const org = await readJSON(`${initial.server}/api/orgs/current`, headers, signal);
      if (org.status !== 200 || org.malformed) result = httpFailure(org);
      else if (!org.data || org.data.org?.id !== initial.org || typeof org.data.userId !== "string" || !org.data.userId) result = failure("workspace_mismatch");
      else {
        const quota = await readJSON(`${initial.server}/api/go/status`, headers, signal);
        if (quota.status !== 200 || quota.malformed) result = httpFailure(quota);
        else if (quota.data === null) result = failure("no_subscription");
        else if (!quota.data || quota.data.subscriberUserId !== org.data.userId) result = failure("owner_mismatch");
        else if (!["go", "go-plus"].includes(quota.data.product)) result = unsupported("plan_unknown");
        else if (quota.data.access === null) result = failure("access_paused");
        else {
          const windows = quotaWindows(quota.data.access?.meters);
          result = windows ? { state: "ok", windows, observed_at: new Date().toISOString() } : failure("schema_changed");
        }
      }
    } catch { result = failure("query_failed"); }
    return await finish(ctx, initial, result);
  } catch { return failure("native_bridge_failed"); }
}

export default {
  id: "wi-quota",
  async setup(ctx) {
    await ctx.rpc.register({
      id: "wi.quota",
      methods: {
        identity: {
          input: { type: "object", additionalProperties: false },
          output: { type: "object", additionalProperties: true }
        },
        read: {
          input: { type: "object", properties: { binding: { type: "string" } }, required: ["binding"], additionalProperties: false },
          output: { type: "object", additionalProperties: true }
        }
      }, events: {}
    }, {
      identity: async () => {
        try {
          const identity = await capture(ctx);
          if (identity.state !== "ok") return identity;
          // Never return the captured credential or native account identifiers.
          return { state: "ok", binding: identity.binding };
        } catch { return failure("native_bridge_failed"); }
      },
      read: async (input, context) => /^[0-9a-f]{64}$/.test(input.binding)
        ? readQuota(ctx, input.binding, context.signal) : failure("identity_missing")
    });
  }
};
