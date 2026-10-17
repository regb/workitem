import { chmodSync, unlinkSync, writeSync } from "node:fs";
import { createHash, randomUUID } from "node:crypto";
import { basename, dirname, join } from "node:path";
import { createConnection, createServer, type Server } from "node:net";
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
  type: "prompt" | "steer" | "follow_up" | "abort" | "shutdown";
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

function textFromContent(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((part): part is { type: string; text?: string } => !!part && typeof part === "object" && "type" in part)
    .filter((part) => part.type === "text" && typeof part.text === "string")
    .map((part) => part.text ?? "")
    .join("\n");
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
  const contentPath = join(dirname(controlSocketPath), createHash("sha256").update(`content:${basename(controlSocketPath)}`).digest("hex").slice(0, 16) + ".sock");
  let contentSequence = 0;
  let contentText = "";
  let contentTruncated = false;
  let contentActivity = "";
  let currentRequestId = "";
  const handled = new Set<string>();
  const runtimeNonce = randomUUID();
  let runtimeSequence = 0;
  let sendingRuntimeEvent = false;
  let runtimeEventsDisabled = false;
  let runtimeRetry: ReturnType<typeof setTimeout> | undefined;
  let idleProbe: ReturnType<typeof setInterval> | undefined;
  let activityReportedBusy = false;
  let settledFailed = false;
  const runtimeQueue: Array<Record<string, unknown>> = [];
  let lastDaemonRejection = "";
  let rejectionLogCooldown = 0;
  const retainedRuntimeTypes = new Set([
    "runtime.ready", "runtime.stopping", "runtime.failed", "command.accepted", "command.rejected",
    "agent.started", "agent.settled", "turn.started", "turn.ended", "message.completed",
    "tool.started", "tool.completed",
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
    contentSequence++;
    if (type === "message.started") { contentText = ""; contentTruncated = false; contentActivity = "Assistant responding"; }
    if (type === "message.delta" && !contentTruncated) {
      const bytes = Buffer.from(contentText + String(fields.text ?? ""), "utf8");
      if (bytes.length > maxContentBytes) {
        let end = maxContentBytes;
        while (end > 0 && (bytes[end] & 0xc0) === 0x80) end--;
        contentText = bytes.subarray(0, end).toString("utf8");
        contentTruncated = true;
      } else contentText = bytes.toString("utf8");
      contentActivity = "Assistant responding";
    }
    if (type === "message.completed") { contentText = ""; contentTruncated = false; contentActivity = "Message saved"; }
    if (type === "tool.started") contentActivity = `Tool: ${String(fields.tool_name ?? "")}`;
    if (type === "agent.settled") { contentText = ""; contentTruncated = false; submittedWork = false; contentActivity = "Idle"; }
    queueRuntimeEvent(record);
  };

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
        default:
          throw new Error(`unsupported runtime control command ${(command as ControlCommand).type}`);
      }
    } catch (error) {
      handled.delete(command.id);
      if (command.type === "prompt" || command.type === "steer" || command.type === "follow_up") submittedWork = false;
      reject(command, error);
      return error instanceof Error ? error.message : String(error);
    }
  };

  pi.on("session_start", (event, ctx) => {
    context = ctx;
    contentServer = createServer((socket) => {
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
          socket.end(`${JSON.stringify({protocol_version: contentVersion, runtime_id: runtimeId, work_item_id: workItemId, sequence: contentSequence, idle_guard: true, text: contentText, truncated: contentTruncated, activity: contentActivity, leaf_id: context?.sessionManager.getLeafId() ?? "-"})}\n`);
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
      try { chmodSync(controlSocketPath, 0o600); } catch { /* best effort */ }
    });
    controlServer.on("error", (error) => emit("runtime.failed", { error: `control socket: ${String(error)}` }));
    if (idleProbe) clearInterval(idleProbe);
    idleProbe = setInterval(reconcileIdle, 1000);
    idleProbe.unref?.();
  });

  pi.on("input", (event, ctx) => {
    context = ctx;
    const marker = event.text.match(/\[wi request ([^\]]+)\]/);
    if (marker?.[1]) currentRequestId = marker[1];
  });
  pi.on("agent_start", (event, ctx) => {
    context = ctx;
    settledFailed = false;
    activityReportedBusy = true;
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
    activityReportedBusy = false;
    currentRequestId = "";
  });
  pi.on("turn_start", (event, ctx) => {
    context = ctx;
    activityReportedBusy = true;
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
    activityReportedBusy = true;
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
  pi.on("session_tree", (event, ctx) => {
    context = ctx;
    emit("session.changed", { data: { old_leaf_id: event.oldLeafId, new_leaf_id: event.newLeafId }, backend_event_type: "session_tree", backend_event: { type: "session_tree", ...event } });
  });
  pi.on("session_shutdown", (event, ctx) => {
    context = ctx;
    if (idleProbe) clearInterval(idleProbe);
    idleProbe = undefined;
    controlServer?.close();
    controlServer = undefined;
    contentServer?.close();
    contentServer = undefined;
    if (ownsContentSocket) { try { unlinkSync(contentPath); } catch { /* absent */ } }
    ownsContentSocket = false;
    try { unlinkSync(controlSocketPath); } catch { /* already removed */ }
    emit("runtime.stopping", { backend_event_type: "session_shutdown", backend_event: { type: "session_shutdown", ...event } });
  });
}
