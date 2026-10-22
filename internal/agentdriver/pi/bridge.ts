import { chmodSync, unlinkSync, writeSync } from "node:fs";
import { createHash, randomUUID } from "node:crypto";
import { basename, dirname, join } from "node:path";
import { createConnection, createServer, type Server, type Socket } from "node:net";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const protocolVersion = 1;
const contentVersion = 1;
const maxContentBytes = 256 * 1024;
const maxControlCommandBytes = 12 * 1024 * 1024;
const maxControlImageBytes = 8 * 1024 * 1024;

interface ControlCommand {
  protocol_version: number;
  id: string;
  runtime_id: string;
  work_item_id: string;
  request_id?: string;
  type: "prompt" | "steer" | "follow_up" | "abort" | "shutdown" | "set";
  setting?: { name: string; value: string };
  actor?: string;
  message?: string;
  images?: Array<{ mime_type: string; data: string }>;
  created_at: string;
  require_idle?: boolean;
}

function validateControlContent(command: ControlCommand) {
  if (command.message !== undefined && (typeof command.message !== "string" || Buffer.byteLength(command.message) > 64 * 1024)) throw new Error("control message must be at most 64 KiB");
  if (command.images !== undefined && !Array.isArray(command.images)) throw new Error("invalid control images");
  const images = command.images ?? [];
  if (images.length > 4) throw new Error("at most four control images are allowed");
  if (images.length && !["prompt", "steer", "follow_up"].includes(command.type)) throw new Error("control command does not accept images");
  let total = 0;
  for (const image of images) {
    if (!image || !["image/jpeg", "image/png", "image/gif", "image/webp"].includes(image.mime_type)) throw new Error("unsupported control image type");
    if (typeof image.data !== "string" || !image.data.length || image.data.length > 4 * Math.ceil(maxControlImageBytes / 3) || !/^[A-Za-z0-9+/]*={0,2}$/.test(image.data)) throw new Error("invalid control image base64 data");
    const decoded = Buffer.from(image.data, "base64");
    if (decoded.toString("base64") !== image.data) throw new Error("invalid control image base64 data");
    total += decoded.length;
    if (total > maxControlImageBytes) throw new Error("control images exceed 8 MiB total");
  }
}

type ContentTool = { id?: string; name: string; status: string; input?: string; output?: string };
const maxFieldBytes = 64 * 1024;

// dataText renders tool input or output as Go's eventDataText does: strings as
// they are, anything else as indented JSON.
function dataText(value: unknown): string {
  if (value === undefined || value === null) return "";
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

// boundedField mirrors driverprotocol's 64 KiB tool field bound.
function boundedField(value: string): string {
  const bytes = Buffer.from(value, "utf8");
  if (bytes.length <= maxFieldBytes) return value;
  let end = maxFieldBytes;
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
  return bytes.subarray(0, end).toString("utf8") + "\n[content truncated]";
}

// boundedThought keeps the newest 64 KiB of reasoning, like driverprotocol.
function boundedThought(value: string): string {
  const bytes = Buffer.from(value, "utf8");
  if (bytes.length <= maxFieldBytes) return value;
  let start = bytes.length - maxFieldBytes;
  while (start < bytes.length && (bytes[start] & 0xc0) === 0x80) start++;
  return bytes.subarray(start).toString("utf8");
}

function textFromContent(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((part): part is { type: string; text?: string } => !!part && typeof part === "object" && "type" in part)
    .filter((part) => part.type === "text" && typeof part.text === "string")
    .map((part) => part.text ?? "")
    .join("\n");
}

type NativeObservation = { agent_state: "active" | "idle" | "failed"; leaf_id: string; readiness: "busy" | "ready" };

// Pi owns branch selection and run finality. Never derive current state from
// all JSONL entries: abandoned branches and omitted recovery attempts remain there.
function nativeObserver(publish: (state: NativeObservation) => void, reserved: () => boolean = () => false) {
  let context: ExtensionContext | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;
  let previous = "";
  const refresh = (ctx: ExtensionContext, force = false, finalFailed?: boolean) => {
    context = ctx;
    const busy = !ctx.isIdle() || ctx.hasPendingMessages() || reserved();
    const leaf = ctx.sessionManager.getLeafId() ?? "-";
    const key = JSON.stringify([busy, leaf]);
    if (!force && key === previous) return;
    let state: NativeObservation["agent_state"] = busy ? "active" : "idle";
    if (!busy && finalFailed !== undefined) state = finalFailed ? "failed" : "idle";
    else if (!busy) {
      const messages = ctx.sessionManager.buildSessionProjection().messages;
      for (let i = messages.length - 1; i >= 0; i--) {
        const message = messages[i];
        if (message.role === "assistant") {
          if (message.stopReason === "error" || message.stopReason === "aborted") state = "failed";
          break;
        }
        if (message.role === "toolResult") { if (message.isError) state = "failed"; break; }
        if (message.role === "user") break;
      }
    }
    publish({ agent_state: state, leaf_id: leaf, readiness: busy ? "busy" : "ready" });
    previous = key;
  };
  const stop = () => { if (timer) clearInterval(timer); timer = undefined; context = undefined; previous = ""; };
  const start = (ctx: ExtensionContext) => {
    stop(); refresh(ctx, true);
    // Reconcile missed notifications using native state, never elapsed-time
    // guesses. Only a changed leaf/run state publishes another compact fact.
    timer = setInterval(() => { if (context) { try { refresh(context); } catch { /* retry after backpressure drains */ } } }, 1000);
    timer.unref?.();
  };
  return { start, refresh, stop };
}

export default function wiBridge(pi: ExtensionAPI) {
  const workItemId = process.env.WI_ID ?? "";
  const runtimeId = process.env.WI_AGENT_RUNTIME_ID ?? "";
  const mode = process.env.WI_AGENT_MODE ?? "";
  const controlSocketPath = process.env.WI_AGENT_CONTROL_SOCKET ?? "";
  const eventSocketPath = process.env.WI_AGENT_EVENT_SOCKET ?? "";
  if (!workItemId || !runtimeId || !controlSocketPath || !eventSocketPath) return;

  // Headless control is transported through RPC, but the idle precondition
  // must be checked in Pi's own event loop, not against delayed host events.
  if (mode === "headless") {
    const observation = nativeObserver(state => writeSync(1, JSON.stringify({ type: "wi_runtime_status", runtime_id: runtimeId, work_item_id: workItemId, ...state }) + "\n"));
    pi.on("session_start", (_event, ctx) => observation.start(ctx));
    pi.on("session_tree", (_event, ctx) => observation.refresh(ctx, true));
    let finalFailed = false;
    pi.on("agent_before_settle", event => { finalFailed = event.outcome === "error" || event.outcome === "aborted"; });
    pi.on("agent_settled", (_event, ctx) => observation.refresh(ctx, true, finalFailed));
    pi.on("session_compact", (_event, ctx) => observation.refresh(ctx, true));
    pi.on("session_compact_failed", (_event, ctx) => observation.refresh(ctx, true));
    pi.on("session_shutdown", () => observation.stop());
    pi.registerCommand("wi-control-idle", {
      description: "Internal wi idle-only runtime control",
      handler: async (args, ctx) => {
        const command = JSON.parse(Buffer.from(args, "base64").toString("utf8")) as ControlCommand;
        // Pi reports slash commands as handled even when a handler throws.
        // Send a separate private result; it is never native conversation content.
        // RPC redirects ordinary stdout writes to diagnostics. This driver-owned
        // record uses fd 1 directly, keeping it in the native RPC transport.
        const result = (success: boolean, error?: string) => writeSync(1, JSON.stringify({
          type: "wi_control_result", id: command.id, runtime_id: runtimeId,
          work_item_id: workItemId, success, error,
        }) + "\n");
        try {
          if (!command.id || command.runtime_id !== runtimeId || command.work_item_id !== workItemId) throw new Error("control owner mismatch");
          if (command.protocol_version !== protocolVersion || !command.require_idle) throw new Error("invalid idle-only control");
          if (!ctx.isIdle() || ctx.hasPendingMessages()) throw new Error("agent became busy; operation refused without interrupting the turn");
          switch (command.type) {
            case "shutdown": ctx.shutdown(); break;
            case "abort": ctx.abort(); break;
            default: throw new Error("unsupported idle-only control");
          }
          result(true);
        } catch (error) {
          result(false, error instanceof Error ? error.message : String(error));
        }
      },
    });
    return;
  }

  let context: ExtensionContext | undefined;
  let submittedWork = false;
  let controlServer: Server | undefined;
  let contentServer: Server | undefined;
  let ownsContentSocket = false;
  let ownsControlSocket = false;
  let shuttingDown = false;
  const connections = new Set<Socket>();
  const trackConnection = (socket: Socket) => { connections.add(socket); socket.on("close", () => connections.delete(socket)); };
  const contentPath = join(dirname(controlSocketPath), createHash("sha256").update(`content:${basename(controlSocketPath)}`).digest("hex").slice(0, 16) + ".sock");
  let contentSequence = 0;
  let contentMessageId = "";
  let contentText = "";
  let contentTruncated = false;
  let contentActivity = "";
  let contentLeaf = "";
  let contentSavedAs: string[] = [];
  let contentThought = "";
  // Prompts submitted with a request ID, oldest first, waiting for the user
  // message that carries them.
  let submittedPrompts: Array<{ requestId: string; text: string }> = [];
  let contentTools: ContentTool[] = [];
  // The most recently changed call, as driverprotocol's Content.Tool.
  let contentTool: ContentTool | undefined;
  let contentPrompts: Array<{ request_id: string; native_ids: string[] }> = [];
  // Content stream subscribers; see agentdriver/protocol/content.go.
  const contentSubscribers = new Set<Socket>();
  const maxContentSubscribers = 32;
  const maxContentBacklog = 1 << 20;
  let currentRequestId = "";
  const handled = new Set<string>();
  const runtimeNonce = randomUUID();
  let runtimeSequence = 0;
  let sendingRuntimeEvent = false;
  let runtimeEventsDisabled = false;
  let runtimeRetry: ReturnType<typeof setTimeout> | undefined;
  let settledFailed = false;
  const runtimeQueue: Array<Record<string, unknown>> = [];
  let lastDaemonRejection = "";
  let rejectionLogCooldown = 0;
  const retainedRuntimeTypes = new Set([
    "runtime.ready", "runtime.stopping", "runtime.failed", "command.accepted", "command.rejected",
    "agent.started", "agent.settled", "turn.started", "turn.ended", "message.completed",
    "tool.started", "tool.completed", "runtime.status",
  ]);

  const drainRuntimeQueue = () => {
    if (!eventSocketPath || runtimeEventsDisabled || sendingRuntimeEvent || runtimeQueue.length === 0) return;
    sendingRuntimeEvent = true;
    const event = runtimeQueue[0];
    const socket = createConnection(eventSocketPath);
    let response = "";
    let finished = false;
    const retry = () => {
      if (finished) return;
      finished = true;
      socket.destroy();
      sendingRuntimeEvent = false;
      if (!runtimeRetry) {
        runtimeRetry = setTimeout(() => { runtimeRetry = undefined; drainRuntimeQueue(); }, 100);
      }
    };
    socket.setTimeout(2000, retry);
    socket.on("connect", () => socket.write(`${JSON.stringify(event)}\n`));
    socket.on("data", (chunk) => {
      response += chunk.toString();
      const newline = response.indexOf("\n");
      if (newline < 0 || finished) return;
      let decoded: { accepted?: boolean; error?: string; retryable?: boolean };
      try {
        decoded = JSON.parse(response.slice(0, newline));
      } catch { retry(); return; }
      if (!decoded.accepted) {
        const message = decoded.error ?? "wi rejected runtime event";
        lastDaemonRejection = message;
        const now = Date.now();
        if (now - rejectionLogCooldown > 5000) {
          rejectionLogCooldown = now;
          console.error(`wi rejected runtime event ${String(event.type)}: ${message}`);
        }
        if (decoded.retryable === true) { retry(); return; }
        finished = true;
        socket.destroy();
        runtimeQueue.length = 0;
        sendingRuntimeEvent = false;
        runtimeEventsDisabled = true;
        return;
      }
      finished = true;
      socket.end();
      runtimeQueue.shift();
      sendingRuntimeEvent = false;
      drainRuntimeQueue();
    });
    socket.on("error", retry);
    socket.on("close", () => { if (!finished) retry(); });
  };

  const queueRuntimeEvent = (record: Record<string, unknown>) => {
    if (!eventSocketPath || runtimeEventsDisabled || !retainedRuntimeTypes.has(String(record.type))) return;
    if (runtimeQueue.length >= 1024) {
      const suffix = lastDaemonRejection ? ` (last rejection: ${lastDaemonRejection})` : "";
      throw new Error(`wi daemon runtime-event buffer is full; refusing to drop semantic events${suffix}`);
    }
    runtimeQueue.push({
      protocol_version: protocolVersion,
      event_id: record.event_id,
      runtime_id: runtimeId,
      work_item_id: workItemId,
      type: record.type,
      timestamp: record.timestamp,
      command_id: record.command_id,
      request_id: record.request_id,
      role: record.role,
      tool_name: record.tool_name,
      outcome: record.outcome,
      agent_state: record.agent_state,
      leaf_id: record.leaf_id,
      readiness: record.readiness,
      failed: record.failed === true || record.type === "runtime.failed" || record.type === "command.rejected",
    });
    drainRuntimeQueue();
  };
  const emit = (type: string, fields: Record<string, unknown> = {}) => {
    const record = {
      protocol_version: protocolVersion,
      event_id: `${runtimeId}:${runtimeNonce}:${++runtimeSequence}`,
      runtime_id: runtimeId,
      work_item_id: workItemId,
      type,
      timestamp: new Date().toISOString(),
      backend: "pi",
      request_id: currentRequestId || undefined,
      ...fields,
    };
    syncContentLeaf();
    // Only assistant messages are live text; prompts and tool results must not
    // reset the reply or its parallel tool calls.
    const assistant = fields.role === "assistant";
    if (type === "message.started" && assistant) publishContent({ type: "message.started", message_id: `live-${contentSequence + 1}`, activity: "Assistant responding" });
    if (type === "message.delta" && !contentTruncated && fields.text) publishContent({ type: "message.delta", message_id: contentMessageId || undefined, text: String(fields.text), activity: "Assistant responding" });
    if (type === "message.completed" && assistant) publishContent({ type: "message.completed", message_id: contentMessageId || undefined, activity: "Message saved" });
    if (type === "tool.started" || type === "tool.updated" || type === "tool.completed") publishTool(type, fields);
    const update = (fields.data as { assistant_message_event?: { type?: string; delta?: string } } | undefined)?.assistant_message_event;
    if (type === "message.delta" && update?.type === "thinking_delta" && update.delta) publishContent({ type: "thought.delta", message_id: contentMessageId || undefined, text: update.delta });
    // Models that keep their reasoning private still say they are thinking.
    if (type === "message.delta" && update?.type === "thinking_start" && contentActivity !== "Thinking") publishContent({ type: "activity", activity: "Thinking", leaf_id: contentLeaf });
    if (type === "agent.settled") { submittedWork = false; publishContent({ type: "turn.settled", activity: "Idle" }); }
    // Native run state: a run that is no longer active ends the live reply.
    if (type === "runtime.status") {
      if (fields.agent_state !== "active") {
        currentRequestId = "";
        publishContent({ type: "turn.settled", activity: fields.agent_state === "failed" ? "Selected branch ended with failure" : "Idle" });
      } else if (!contentActivity || contentActivity === "Idle") publishContent({ type: "activity", activity: "Agent working", leaf_id: contentLeaf });
    }
    queueRuntimeEvent(record);
  };

  const observation = nativeObserver(state => {
    emit("runtime.status", state);
    if (state.agent_state !== "active") settledFailed = state.agent_state === "failed";
  }, () => submittedWork);

  // publishTool mirrors the Go content server's tool events: each carries the
  // call's whole state, with input and output as text bounded like Go's.
  const publishTool = (type: string, fields: Record<string, unknown>) => {
    const data = (fields.data ?? {}) as { args?: unknown; partial_result?: unknown; result?: unknown; is_error?: boolean };
    const id = String(fields.tool_call_id ?? ""), name = String(fields.tool_name ?? "");
    const prior = contentTools.findLast((tool) => (id ? tool.id === id : !tool.id && tool.name === name));
    if (type === "tool.started") {
      publishContent({ type, tool: { id: id || undefined, name, status: "running", input: boundedField(dataText(data.args)) || undefined }, activity: `Tool running: ${name}` });
      return;
    }
    const tool: ContentTool = prior ? { ...prior } : { id: id || undefined, name, status: "running" };
    if (type === "tool.completed") tool.status = data.is_error ? "failed" : "completed";
    tool.output = boundedField(dataText(type === "tool.completed" ? data.result : data.partial_result)) || undefined;
    publishContent({ type, tool, activity: type === "tool.completed" ? `Tool ${tool.status}: ${name}` : `Tool running: ${name}` });
  };

  // applyContent mirrors driverprotocol.ApplyContent, so subscribers applying
  // these events hold the same snapshot as this bridge.
  const applyContent = (event: Record<string, unknown>) => {
    const activity = typeof event.activity === "string" ? event.activity : "";
    switch (event.type) {
      case "message.started":
        contentMessageId = String(event.message_id ?? ""); contentText = ""; contentTruncated = false; contentSavedAs = []; contentThought = ""; contentTools = []; contentTool = undefined; break;
      case "tool.started": case "tool.updated": case "tool.completed": {
        const tool = event.tool as ContentTool;
        const at = contentTools.findLastIndex((other) => tool.id ? other.id === tool.id : !other.id && other.name === tool.name);
        contentTools = at >= 0 ? contentTools.map((other, index) => (index === at ? { ...tool } : other)) : [...contentTools, { ...tool }].slice(-32);
        contentTool = { ...tool };
        break;
      }
      case "thought.delta":
        contentThought = boundedThought(contentThought + String(event.text ?? "")); break;
      case "prompt.saved":
        contentPrompts = [...contentPrompts.filter((link) => link.request_id !== event.request_id), { request_id: String(event.request_id ?? ""), native_ids: (event.native_ids as unknown[]).map(String) }].slice(-8); break;
      case "message.saved":
        contentSavedAs = Array.isArray(event.native_ids) ? event.native_ids.map(String) : []; break;
      case "message.delta": {
        if (contentTruncated) break;
        contentSavedAs = [];
        const bytes = Buffer.from(contentText + String(event.text ?? ""), "utf8");
        if (bytes.length > maxContentBytes) {
          let end = maxContentBytes;
          while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
          contentText = bytes.subarray(0, end).toString("utf8");
          contentTruncated = true;
        } else contentText = bytes.toString("utf8");
        break;
      }
      case "activity":
        contentActivity = activity; contentLeaf = String(event.leaf_id ?? ""); return;
    }
    if (activity) contentActivity = activity;
  };
  // publishContent numbers one content event, applies it, and sends it to every
  // subscriber. A subscriber that cannot keep up is dropped and resubscribes.
  const publishContent = (event: Record<string, unknown>) => {
    const numbered = { sequence: ++contentSequence, ...event };
    applyContent(numbered);
    const line = `${JSON.stringify(numbered)}\n`;
    for (const socket of contentSubscribers) {
      if (socket.writableLength > maxContentBacklog) { contentSubscribers.delete(socket); socket.destroy(); continue; }
      socket.write(line);
    }
  };
  // The branch leaf lives in Pi's session; a change becomes an activity event.
  const syncContentLeaf = () => {
    let leaf = "-";
    try { leaf = context?.sessionManager?.getLeafId?.() ?? "-"; } catch { /* keep the last known leaf */ return; }
    if (leaf !== contentLeaf) publishContent({ type: "activity", activity: contentActivity, leaf_id: leaf });
  };
  // findEntry returns the newest session entry of a message, by its role and
  // timestamp, looking back a bounded way along the current branch.
  const findEntry = (role: string, timestamp: number) => {
    const branch: Array<{ type?: string; id?: string; message?: { role?: string; timestamp?: number } }> = context?.sessionManager?.getBranch?.() ?? [];
    for (let i = branch.length - 1; i >= Math.max(0, branch.length - 64); i--) {
      const entry = branch[i];
      if (entry.type === "message" && entry.message?.role === role && entry.message.timestamp === timestamp) return entry.id ?? "";
    }
    return "";
  };
  // afterAppend looks for a session entry Pi appends after dispatching an
  // event, retrying briefly, and reports it once found.
  const afterAppend = (find: () => string, found: (id: string) => void) => {
    const delays = [0, 50, 250, 1000, 3000];
    const attempt = (index: number) => setTimeout(() => {
      let id = "";
      try { id = find(); } catch { return; }
      if (id) found(id);
      else if (index + 1 < delays.length) attempt(index + 1);
    }, delays[index]);
    attempt(0);
  };
  // linkPrompt pairs a saved user message with the oldest submitted prompt of
  // the same text and reports the entry holding it to the prompt's sender.
  const linkPrompt = (message: { role?: string; timestamp?: number; content?: unknown }) => {
    if (message.role !== "user" || typeof message.timestamp !== "number") return;
    const text = textFromContent(message.content).trim();
    const index = submittedPrompts.findIndex((prompt) => prompt.text === text);
    if (index < 0) return;
    const { requestId } = submittedPrompts[index];
    submittedPrompts = submittedPrompts.filter((_, other) => other !== index);
    afterAppend(() => findEntry("user", message.timestamp!), (id) => publishContent({ type: "prompt.saved", request_id: requestId, native_ids: [id] }));
  };
  // markSaved reports the session entry holding an assistant message. Pi appends
  // it after dispatching message_end, so the lookup waits for the next task;
  // it is published only while the live text is still that message's text.
  const markSaved = (message: { role?: string; timestamp?: number; content?: unknown }) => {
    const text = textFromContent(message.content).trim();
    if (message.role !== "assistant" || !text || typeof message.timestamp !== "number") return;
    afterAppend(() => findEntry("assistant", message.timestamp!), (id) => {
      // Whitespace may differ between streamed and saved text.
      if (contentText.split(/\s+/).join(" ").trim() !== text.split(/\s+/).join(" ").trim()) return;
      syncContentLeaf();
      publishContent({ type: "message.saved", message_id: contentMessageId || undefined, native_ids: [id] });
    });
  };
  const contentSnapshot = () => ({ protocol_version: contentVersion, runtime_id: runtimeId, work_item_id: workItemId, sequence: contentSequence, idle_guard: true, message_id: contentMessageId || undefined, text: contentText, truncated: contentTruncated, activity: contentActivity, leaf_id: contentLeaf, saved_as: contentSavedAs.length ? contentSavedAs : undefined, thought: contentThought || undefined, tool: contentTool, tools: contentTools.length ? contentTools : undefined, prompts: contentPrompts.length ? contentPrompts : undefined });

  const reconcileIdle = () => {
    if (!activityReportedBusy || !context?.isIdle()) return;
    try {
      emit("agent.settled", { data: { reconciled: true }, failed: settledFailed, outcome: settledFailed ? "failed" : "completed", backend_event_type: "idle_probe" });
      activityReportedBusy = false;
      currentRequestId = "";
    } catch {
      // A full runtime queue throws rather than dropping semantic events. Keep
      // the busy marker set so the next probe retries after the queue drains.
    }
  };

  const accept = (command: ControlCommand) => emit("command.accepted", {
    command_id: command.id,
    request_id: command.request_id,
    data: { command_type: command.type, actor: command.actor },
  });

  const reject = (command: ControlCommand, error: unknown) => emit("command.rejected", {
    command_id: command.id,
    request_id: command.request_id,
    error: error instanceof Error ? error.message : String(error),
    data: { command_type: command.type, actor: command.actor },
  });

  const execute = async (command: ControlCommand): Promise<string | undefined> => {
    if (command.protocol_version !== protocolVersion) throw new Error(`unsupported control protocol ${command.protocol_version}`);
    if (command.runtime_id !== runtimeId) throw new Error(`control command targets runtime ${command.runtime_id || "<missing>"}, expected ${runtimeId}`);
    if (command.work_item_id !== workItemId) throw new Error(`control command targets work item ${command.work_item_id || "<missing>"}, expected ${workItemId}`);
    if (!command.id) throw new Error("control command id is required");
    if (handled.has(command.id)) return;
    validateControlContent(command);
    if (command.require_idle && (submittedWork || !context?.isIdle() || context.hasPendingMessages())) throw new Error("agent became busy; operation refused without interrupting the turn");
    handled.add(command.id);
    try {
      switch (command.type) {
        case "prompt":
        case "steer":
        case "follow_up": {
          if (!command.message?.trim() && !command.images?.length) throw new Error("runtime control message is empty");
          const content = command.images?.length
            ? [...(command.message?.trim() ? [{ type: "text" as const, text: command.message }] : []),
                ...command.images.map(image => ({ type: "image" as const, mimeType: image.mime_type, data: image.data }))]
            : command.message!;
          submittedWork = true;
          if (command.request_id) submittedPrompts = [...submittedPrompts, { requestId: command.request_id, text: command.message?.trim() ?? "" }].slice(-16);
          if (context?.isIdle()) {
            pi.sendUserMessage(content);
          } else {
            const deliverAs = command.type === "follow_up" ? "followUp" : "steer";
            pi.sendUserMessage(content, { deliverAs });
          }
          accept(command);
          break;
        }
        case "abort":
          if (!context) throw new Error("Pi extension context is not ready");
          context.abort();
          submittedWork = false;
          accept(command);
          break;
        case "shutdown":
          if (!context) throw new Error("Pi extension context is not ready");
          context.shutdown();
          accept(command);
          break;
        case "set": {
          if (!context) throw new Error("Pi extension context is not ready");
          const setting = command.setting;
          if (!setting || typeof setting.name !== "string" || typeof setting.value !== "string") throw new Error("control set requires a setting");
          if (setting.name === "model") {
            const slash = setting.value.indexOf("/");
            if (slash <= 0 || slash === setting.value.length - 1) throw new Error(`Pi model must be provider/model, got ${setting.value}`);
            const model = context.modelRegistry.find(setting.value.slice(0, slash), setting.value.slice(slash + 1));
            if (!model) throw new Error(`unknown Pi model ${setting.value}`);
            if (!(await pi.setModel(model))) throw new Error(`no credentials are configured for ${setting.value}`);
          } else if (setting.name === "thinking") {
            if (!["off", "minimal", "low", "medium", "high", "xhigh", "max"].includes(setting.value)) throw new Error(`invalid Pi thinking level ${setting.value}`);
            pi.setThinkingLevel(setting.value as Parameters<typeof pi.setThinkingLevel>[0]);
          } else {
            throw new Error(`Pi cannot change ${setting.name} while running`);
          }
          accept(command);
          break;
        }
        default:
          throw new Error(`unsupported runtime control command ${(command as ControlCommand).type}`);
      }
    } catch (error) {
      handled.delete(command.id);
      if (command.type === "prompt" || command.type === "steer" || command.type === "follow_up") submittedWork = false;
      // A refused prompt must not be paired with a later one of the same text.
      if (command.request_id) submittedPrompts = submittedPrompts.filter((prompt) => prompt.requestId !== command.request_id);
      reject(command, error);
      return error instanceof Error ? error.message : String(error);
    }
  };

  pi.on("session_start", (event, ctx) => {
    context = ctx;
    contentServer = createServer((socket) => {
      trackConnection(socket);
      let request = "";
      socket.setEncoding("utf8");
      socket.setTimeout(2000, () => socket.destroy());
      socket.on("error", () => socket.destroy());
      socket.on("data", (chunk) => {
        request += chunk.toString();
        if (request.length > 4096) { socket.destroy(); return; }
        if (!request.includes("\n")) return;
        try {
          const req = JSON.parse(request.split("\n")[0]);
          if (req.protocol_version !== contentVersion || req.runtime_id !== runtimeId || req.work_item_id !== workItemId) {
            socket.end(`${JSON.stringify({error: "content owner mismatch"})}\n`); return;
          }
          syncContentLeaf();
          if (!req.stream) { socket.end(`${JSON.stringify(contentSnapshot())}\n`); return; }
          if (contentSubscribers.size >= maxContentSubscribers) { socket.end(`${JSON.stringify({error: "too many content subscribers"})}\n`); return; }
          // A stream starts with a snapshot and stays open for later events.
          socket.removeAllListeners("data");
          socket.setTimeout(0);
          socket.on("close", () => contentSubscribers.delete(socket));
          contentSubscribers.add(socket);
          socket.write(`${JSON.stringify({ sequence: contentSequence, type: "snapshot", snapshot: contentSnapshot() })}\n`);
        } catch { socket.destroy(); }
      });
    });
    contentServer.on("error", (error) => console.error(`wi content socket: ${String(error)}`));
    contentServer.listen(contentPath, () => { ownsContentSocket = true; try { chmodSync(contentPath, 0o600); } catch { /* listen error reported above */ } });
    emit("runtime.ready", {
      data: { mode, session_id: ctx.sessionManager.getSessionId(), session_file: ctx.sessionManager.getSessionFile() },
      backend_event_type: "session_start",
      backend_event: { type: "session_start", ...event },
    });
    try { unlinkSync(controlSocketPath); } catch { /* absent or stale socket cleanup failed; listen reports real errors */ }
    controlServer = createServer((socket) => {
      trackConnection(socket);
      let buffered = "";
      let bufferedBytes = 0;
      socket.setTimeout(2000, () => socket.destroy());
      socket.on("error", () => socket.destroy());
      socket.setEncoding("utf8");
      socket.on("data", (chunk) => {
        buffered += chunk;
        bufferedBytes += Buffer.byteLength(chunk);
        if (bufferedBytes > maxControlCommandBytes + 1) { socket.destroy(); return; }
        if (!chunk.includes("\n")) return;
        const newline = buffered.indexOf("\n");
        const raw = buffered.slice(0, newline).trim();
        if (Buffer.byteLength(raw) > maxControlCommandBytes) { socket.destroy(); return; }
        buffered = "";
        bufferedBytes = 0;
        void (async () => {
          try {
            const error = await execute(JSON.parse(raw) as ControlCommand);
            socket.end(`${JSON.stringify({ accepted: !error, error })}\n`);
          } catch (error) {
            socket.end(`${JSON.stringify({ accepted: false, error: String(error) })}\n`);
          }
        })();
      });
    });
    controlServer.listen(controlSocketPath, () => {
      ownsControlSocket = true;
      try { chmodSync(controlSocketPath, 0o600); } catch { /* best effort */ }
    });
    controlServer.on("error", (error) => emit("runtime.failed", { error: `control socket: ${String(error)}` }));
    observation.start(ctx);
  });

  pi.on("input", (event, ctx) => {
    context = ctx;
    const marker = event.text.match(/\[wi request ([^\]]+)\]/);
    if (marker?.[1]) currentRequestId = marker[1];
  });
  pi.on("agent_start", (event, ctx) => {
    context = ctx;
    // Pi has accepted the handoff; native run state now guards idle control.
    submittedWork = false;
    settledFailed = false;
    emit("agent.started", { backend_event_type: "agent_start", backend_event: { type: "agent_start", ...event } });
  });
  pi.on("agent_end", (event, ctx) => {
    context = ctx;
    emit("agent.ended", { backend_event_type: "agent_end", backend_event: { type: "agent_end", ...event } });
  });
  pi.on("agent_before_settle", (event, ctx) => {
    context = ctx;
    settledFailed = event.outcome === "error" || event.outcome === "aborted";
  });
  pi.on("agent_settled", (event, ctx) => {
    context = ctx;
    emit("agent.settled", { failed: settledFailed, outcome: settledFailed ? "failed" : "completed", backend_event_type: "agent_settled", backend_event: { type: "agent_settled", ...event } });
    currentRequestId = "";
    observation.refresh(ctx, true, settledFailed);
  });
  pi.on("turn_start", (event, ctx) => {
    context = ctx;
    emit("turn.started", { data: { turn_index: event.turnIndex }, backend_event_type: "turn_start", backend_event: { type: "turn_start", ...event } });
  });
  pi.on("turn_end", (event, ctx) => {
    context = ctx;
    emit("turn.ended", { data: { turn_index: event.turnIndex, message: event.message, tool_results: event.toolResults }, backend_event_type: "turn_end", backend_event: { type: "turn_end", ...event } });
  });
  pi.on("message_start", (event, ctx) => {
    context = ctx;
    emit("message.started", { role: event.message.role, backend_event_type: "message_start", backend_event: { type: "message_start", message: event.message } });
  });
  pi.on("message_update", (event, ctx) => {
    context = ctx;
    const update = event.assistantMessageEvent;
    // Extension events include a cumulative `partial` snapshot. Pi's native
    // JSON/RPC stream deliberately removes it so stream size stays linear.
    const { partial: _partial, ...deltaEvent } = update;
    emit("message.delta", {
      role: "assistant",
      text: deltaEvent.type === "text_delta" ? deltaEvent.delta : undefined,
      data: { assistant_message_event: deltaEvent },
      backend_event_type: "message_update",
      backend_event: { type: "message_update", assistantMessageEvent: deltaEvent },
    });
  });
  pi.on("message_end", (event, ctx) => {
    context = ctx;
    const message = event.message;
    markSaved(message);
    linkPrompt(message);
    if (message.role === "assistant") settledFailed = message.stopReason === "error" || message.stopReason === "aborted";
    emit("message.completed", {
      role: message.role,
      text: message.role === "assistant" ? textFromContent(message.content) : undefined,
      data: { stop_reason: message.role === "assistant" ? message.stopReason : undefined, message },
      backend_event_type: "message_end",
      backend_event: { type: "message_end", message },
    });
  });
  pi.on("tool_execution_start", (event, ctx) => {
    context = ctx;
    emit("tool.started", {
      tool_call_id: event.toolCallId,
      tool_name: event.toolName,
      data: { args: event.args },
      backend_event_type: "tool_execution_start",
      backend_event: { type: "tool_execution_start", ...event },
    });
  });
  pi.on("tool_execution_update", (event, ctx) => {
    context = ctx;
    emit("tool.updated", {
      tool_call_id: event.toolCallId,
      tool_name: event.toolName,
      data: { args: event.args, partial_result: event.partialResult },
      backend_event_type: "tool_execution_update",
      backend_event: { type: "tool_execution_update", ...event },
    });
  });
  pi.on("tool_execution_end", (event, ctx) => {
    context = ctx;
    emit("tool.completed", {
      tool_call_id: event.toolCallId,
      tool_name: event.toolName,
      data: { result: event.result, is_error: event.isError },
      backend_event_type: "tool_execution_end",
      backend_event: { type: "tool_execution_end", ...event },
    });
  });
  pi.on("queue_update", (event, ctx) => {
    context = ctx;
    emit("queue.changed", { data: { steering: event.steering, follow_up: event.followUp }, backend_event_type: "queue_update", backend_event: { type: "queue_update", ...event } });
  });
  pi.on("compaction_start", (event, ctx) => {
    context = ctx;
    emit("backend.compaction.start", { backend_event_type: "compaction_start", backend_event: { type: "compaction_start", ...event } });
  });
  pi.on("compaction_end", (event, ctx) => {
    context = ctx;
    emit("backend.compaction.end", { backend_event_type: "compaction_end", backend_event: { type: "compaction_end", ...event } });
  });
  pi.on("auto_retry_start", (event, ctx) => {
    context = ctx;
    emit("backend.auto.retry.start", { backend_event_type: "auto_retry_start", backend_event: { type: "auto_retry_start", ...event } });
  });
  pi.on("auto_retry_end", (event, ctx) => {
    context = ctx;
    emit("backend.auto.retry.end", { backend_event_type: "auto_retry_end", backend_event: { type: "auto_retry_end", ...event } });
  });
  pi.on("session_before_switch", (event, ctx) => {
    context = ctx;
    emit("session.changed", { data: { blocked: true, operation: event.reason, target_session_file: event.targetSessionFile }, backend_event_type: "session_before_switch", backend_event: { type: "session_before_switch", ...event } });
    ctx.ui.notify("This Pi conversation is owned by a wi work item; start a new wi item instead of switching its root session.", "warning");
    return { cancel: true };
  });
  pi.on("session_before_fork", (event, ctx) => {
    context = ctx;
    emit("session.changed", { data: { blocked: true, operation: "fork", entry_id: event.entryId }, backend_event_type: "session_before_fork", backend_event: { type: "session_before_fork", ...event } });
    ctx.ui.notify("Forking the wi-owned root conversation is disabled; use /tree or create another work item.", "warning");
    return { cancel: true };
  });
  pi.on("session_compact", (_event, ctx) => { context = ctx; observation.refresh(ctx, true); });
  pi.on("session_compact_failed", (_event, ctx) => { context = ctx; observation.refresh(ctx, true); });
  pi.on("session_tree", (event, ctx) => {
    context = ctx;
    submittedWork = false;
    observation.refresh(ctx, true);
  });
  pi.on("session_shutdown", async (event, ctx) => {
    if (shuttingDown) return;
    shuttingDown = true;
    context = ctx;
    observation.stop();
    const servers = [controlServer, contentServer];
    controlServer = undefined;
    // Subscribers stay connected until dropped; the server closes after.
    for (const socket of contentSubscribers) socket.destroy();
    contentSubscribers.clear();
    contentServer = undefined;
    // Reload must not start a new listener before the old Unix server has
    // finished unlinking its endpoint.
    // A stalled local client must not hold /reload open indefinitely.
    const drain = setTimeout(() => { for (const socket of connections) socket.destroy(); }, 1000);
    await Promise.all(servers.map(server => new Promise<void>(resolve => {
      if (!server) { resolve(); return; }
      try { server.close(() => resolve()); } catch { resolve(); }
    })));
    clearTimeout(drain);
    if (ownsContentSocket) { try { unlinkSync(contentPath); } catch { /* absent */ } }
    ownsContentSocket = false;
    if (ownsControlSocket) { try { unlinkSync(controlSocketPath); } catch { /* already removed */ } }
    ownsControlSocket = false;
    emit("runtime.stopping", { backend_event_type: "session_shutdown", backend_event: { type: "session_shutdown", ...event } });
  });
}
