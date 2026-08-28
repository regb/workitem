import { chmodSync, unlinkSync } from "node:fs";
import { randomUUID } from "node:crypto";
import { createConnection, createServer, type Server } from "node:net";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";

const protocolVersion = 1;

interface ControlCommand {
  protocol_version: number;
  id: string;
  runtime_id: string;
  work_item_id: string;
  request_id?: string;
  type: "prompt" | "steer" | "follow_up" | "abort" | "shutdown";
  actor?: string;
  message?: string;
  created_at: string;
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
  const daemonSocketPath = process.env.WI_DAEMON_SOCKET ?? "";
  const daemonProtocolVersion = Number(process.env.WI_DAEMON_PROTOCOL ?? "0");
  const daemonBuildIdentity = process.env.WI_BUILD_IDENTITY ?? "";
  if (!workItemId || !runtimeId || !controlSocketPath || !daemonSocketPath || daemonProtocolVersion <= 0 || !daemonBuildIdentity) return;

  let context: ExtensionContext | undefined;
  let controlServer: Server | undefined;
  let currentRequestId = "";
  const handled = new Set<string>();
  const runtimeNonce = randomUUID();
  let runtimeSequence = 0;
  let sendingRuntimeEvent = false;
  let runtimeEventsDisabled = false;
  let runtimeRetry: ReturnType<typeof setTimeout> | undefined;
  const runtimeQueue: Array<Record<string, unknown>> = [];
  let lastDaemonRejection = "";
  let rejectionLogCooldown = 0;
  const retainedRuntimeTypes = new Set([
    "runtime.ready", "runtime.stopping", "runtime.failed", "command.accepted", "command.rejected",
    "agent.started", "agent.settled", "turn.started", "turn.ended", "message.completed",
    "tool.started", "tool.completed",
  ]);

  const drainRuntimeQueue = () => {
    if (!daemonSocketPath || runtimeEventsDisabled || sendingRuntimeEvent || runtimeQueue.length === 0) return;
    sendingRuntimeEvent = true;
    const event = runtimeQueue[0];
    const requestId = `runtime-event:${String(event.id)}`;
    const socket = createConnection(daemonSocketPath);
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
    socket.on("connect", () => socket.write(`${JSON.stringify({ protocol_version: daemonProtocolVersion, build_identity: daemonBuildIdentity, id: requestId, method: "runtime_event", payload: event })}\n`));
    socket.on("data", (chunk) => {
      response += chunk.toString();
      const newline = response.indexOf("\n");
      if (newline < 0 || finished) return;
      let decoded: { ok?: boolean; error?: { code?: string; message?: string } };
      try {
        decoded = JSON.parse(response.slice(0, newline));
      } catch { retry(); return; }
      if (!decoded.ok) {
        const code = decoded.error?.code ?? "unknown_error";
        const message = decoded.error?.message ?? "daemon rejected runtime event";
        lastDaemonRejection = `${code}: ${message}`;
        const now = Date.now();
        if (now - rejectionLogCooldown > 5000) {
          rejectionLogCooldown = now;
          console.error(`wi daemon rejected runtime event ${String(event.type)} (${code}): ${message}`);
        }
        if (code === "build_mismatch" || code === "protocol_mismatch" || code === "runtime_owner_mismatch") {
          // This runtime cannot become compatible without a restart. Report the
          // rejection once, then stop sending instead of retrying forever.
          finished = true;
          socket.destroy();
          runtimeQueue.length = 0;
          sendingRuntimeEvent = false;
          runtimeEventsDisabled = true;
          return;
        }
        retry();
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
    if (!daemonSocketPath || runtimeEventsDisabled || !retainedRuntimeTypes.has(String(record.type))) return;
    if (runtimeQueue.length >= 1024) {
      const suffix = lastDaemonRejection ? ` (last rejection: ${lastDaemonRejection})` : "";
      throw new Error(`wi daemon runtime-event buffer is full; refusing to drop semantic events${suffix}`);
    }
    runtimeQueue.push({
      id: record.event_id,
      runtime_id: runtimeId,
      work_item_id: workItemId,
      type: record.type,
      timestamp: record.timestamp,
      request_id: record.request_id,
      role: record.role,
      tool_name: record.tool_name,
      failed: record.type === "runtime.failed" || record.type === "command.rejected",
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
    queueRuntimeEvent(record);
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
    handled.add(command.id);
    try {
      switch (command.type) {
        case "prompt":
        case "steer":
        case "follow_up": {
          if (!command.message?.trim()) throw new Error("runtime control message is empty");
          if (context?.isIdle()) {
            pi.sendUserMessage(command.message);
          } else {
            const deliverAs = command.type === "follow_up" ? "followUp" : "steer";
            pi.sendUserMessage(command.message, { deliverAs });
          }
          accept(command);
          break;
        }
        case "abort":
          if (!context) throw new Error("Pi extension context is not ready");
          context.abort();
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
      reject(command, error);
      return error instanceof Error ? error.message : String(error);
    }
  };

  pi.on("session_start", (event, ctx) => {
    context = ctx;
    emit("runtime.ready", {
      data: { mode, session_id: ctx.sessionManager.getSessionId(), session_file: ctx.sessionManager.getSessionFile() },
      backend_event_type: "session_start",
      backend_event: { type: "session_start", ...event },
    });
    try { unlinkSync(controlSocketPath); } catch { /* absent or stale socket cleanup failed; listen reports real errors */ }
    controlServer = createServer((socket) => {
      let buffered = "";
      socket.setEncoding("utf8");
      socket.on("data", (chunk) => {
        buffered += chunk;
        const newline = buffered.indexOf("\n");
        if (newline < 0) return;
        const raw = buffered.slice(0, newline).trim();
        buffered = "";
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
  });

  pi.on("input", (event, ctx) => {
    context = ctx;
    const marker = event.text.match(/\[wi request ([^\]]+)\]/);
    if (marker?.[1]) currentRequestId = marker[1];
  });
  pi.on("agent_start", (event, ctx) => {
    context = ctx;
    emit("agent.started", { backend_event_type: "agent_start", backend_event: { type: "agent_start", ...event } });
  });
  pi.on("agent_end", (event, ctx) => {
    context = ctx;
    emit("agent.ended", { backend_event_type: "agent_end", backend_event: { type: "agent_end", ...event } });
  });
  pi.on("agent_settled", (event, ctx) => {
    context = ctx;
    emit("agent.settled", { backend_event_type: "agent_settled", backend_event: { type: "agent_settled", ...event } });
    currentRequestId = "";
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
  pi.on("session_tree", (event, ctx) => {
    context = ctx;
    emit("session.changed", { data: { old_leaf_id: event.oldLeafId, new_leaf_id: event.newLeafId }, backend_event_type: "session_tree", backend_event: { type: "session_tree", ...event } });
  });
  pi.on("session_shutdown", (event, ctx) => {
    context = ctx;
    controlServer?.close();
    controlServer = undefined;
    try { unlinkSync(controlSocketPath); } catch { /* already removed */ }
    emit("runtime.stopping", { backend_event_type: "session_shutdown", backend_event: { type: "session_shutdown", ...event } });
  });
}
