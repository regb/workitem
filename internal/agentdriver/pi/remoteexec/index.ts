import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import path from "node:path";
import {
 type ExtensionAPI, type BashOperations, type EditOperations,
 createReadToolDefinition, createWriteToolDefinition, createEditToolDefinition,
 createBashToolDefinition, createFindToolDefinition, createGrepToolDefinition,
 createLsToolDefinition, truncateHead,
} from "@earendil-works/pi-coding-agent";

// This extension has no local fallback. The runtime socket binds every request
// to one host-owned claim; paths supplied by the model are always executor paths.
export default function executorTools(pi: ExtensionAPI) {
 const executable = process.env.WI_WORKSPACE_RUNTIME;
 const target = process.env.WI_WORKSPACE_TARGET;
 const cwd = process.env.WI_WORKSPACE_REMOTE_CWD;
 if (!executable || !target || !cwd) throw new Error("Wi executor environment is incomplete");
 const remoteCwd = cwd;
 const remotePath = (value: string) => path.posix.resolve(remoteCwd, value);

 function run(args: string[], input?: string | Buffer, signal?: AbortSignal): Promise<{code: number; out: Buffer; err: Buffer}> {
  return new Promise((resolve, reject) => {
   if (signal?.aborted) return reject(new Error("aborted"));
   const child = spawn(executable!, ["internal-workspace-runtime", ...args], {stdio: ["pipe", "pipe", "pipe"]});
   const out: Buffer[] = [], err: Buffer[] = [];
   const abort = () => child.kill("SIGTERM");
   signal?.addEventListener("abort", abort, {once: true});
   child.stdout.on("data", data => out.push(data));
   child.stderr.on("data", data => err.push(data));
   child.stdin.on("error", () => {});
   child.stdin.end(input);
   child.on("error", reject);
   child.on("close", code => {
    signal?.removeEventListener("abort", abort);
    if (signal?.aborted) reject(new Error("aborted"));
    else resolve({code: code ?? 1, out: Buffer.concat(out), err: Buffer.concat(err)});
   });
  });
 }
 async function exec(command: string[], input?: string | Buffer, signal?: AbortSignal) {
  const result = await run(["container", "exec", "-i", "-w", remoteCwd, target!, ...command], input, signal);
  if (result.code !== 0) throw new Error(result.err.toString() || `Executor command exited ${result.code}`);
  return result.out;
 }
 const exists = async (p: string) => (await run(["container", "exec", target!, "test", "-e", p])).code === 0;
 const read = {
  readFile: (p: string) => exec(["cat", p]),
  access: async (p: string) => { await exec(["test", "-r", p]); },
  detectImageMimeType: async (p: string) => {
   const mime = (await exec(["file", "--mime-type", "-b", p])).toString().trim();
   return ["image/png", "image/jpeg", "image/gif", "image/webp"].includes(mime) ? mime : null;
  },
 };
 // Replacement is atomic and preserves permissions. Edit additionally checks
 // the bytes it read. Neither operation is blindly retried after disconnect.
 const atomicWrite = async (p: string, content: string, expected = "") => {
  await exec(["sh", "-c", `set -eu
p=$1; expected=$2; desired=$3
[ ! -L "$p" ] || { echo 'Refusing to replace a symlink' >&2; exit 1; }
tmp=$(mktemp "$(dirname "$p")/.wi-write.XXXXXX")
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
actual=$(sha256sum < "$tmp"); actual=\${actual%% *}
[ "$actual" = "$desired" ] || { echo 'Incomplete write; original file retained' >&2; exit 1; }
if [ -e "$p" ]; then chmod --reference="$p" "$tmp"; else chmod 644 "$tmp"; fi
if [ -n "$expected" ]; then
 actual=$(sha256sum < "$p"); actual=\${actual%% *}
 [ "$actual" = "$expected" ] || { echo 'File changed since edit read it; read and retry' >&2; exit 1; }
fi
mv -f "$tmp" "$p"`, "wi-write", p, expected, createHash("sha256").update(content).digest("hex")], content);
 };
 const write = {
  writeFile: (p: string, content: string) => atomicWrite(p, content),
  mkdir: async (p: string) => { await exec(["mkdir", "-p", p]); },
 };
 pi.registerTool(createReadToolDefinition(remoteCwd, {operations: read}));
 pi.registerTool(createWriteToolDefinition(remoteCwd, {operations: write}));
 const editDefinition = createEditToolDefinition(remoteCwd);
 pi.registerTool({...editDefinition, async execute(id, params, signal, update, ctx) {
  const hashes = new Map<string, string>();
  const operations: EditOperations = {
   access: read.access,
   readFile: async p => { const data = await read.readFile(p); hashes.set(p, createHash("sha256").update(data).digest("hex")); return data; },
   writeFile: async (p, content) => {
    const hash = hashes.get(p); if (!hash) throw new Error("Edit must read before writing");
    await atomicWrite(p, content, hash);
   },
  };
  return createEditToolDefinition(remoteCwd, {operations}).execute(id, params, signal, update, ctx);
 }});
 const bash: BashOperations = {
  exec: (command, _cwd, {onData, signal, timeout}) => new Promise((resolve, reject) => {
   if (signal?.aborted) return reject(new Error("aborted"));
   const id = randomUUID();
   const child = spawn(executable!, ["internal-workspace-runtime", "job", "run", id, String(Math.ceil((timeout ?? 0)*1000)), remoteCwd, command], {stdio: ["ignore", "pipe", "pipe"]});
   let cancellation: Promise<unknown> | undefined;
   const abort = () => { cancellation = run(["job", "cancel", id]).finally(() => child.kill("SIGTERM")); };
   signal?.addEventListener("abort", abort, {once: true});
   child.stdout.on("data", onData); child.stderr.on("data", onData);
   child.on("error", reject);
   child.on("close", async code => {
    signal?.removeEventListener("abort", abort);
    try {
     await cancellation;
     if (signal?.aborted) reject(new Error("aborted"));
     else if (code === 124) reject(new Error(`timeout:${timeout}`));
     else resolve({exitCode: code});
    } catch (error) { reject(error); }
   });
  }),
 };
 pi.registerTool(createBashToolDefinition(remoteCwd, {operations: bash}));
 pi.on("user_bash", () => ({operations: bash}));
 pi.registerTool(createLsToolDefinition(remoteCwd, {operations: {
  exists,
  stat: async p => {
   const directory = (await run(["container", "exec", target!, "test", "-d", p])).code === 0;
   return {isDirectory: () => directory, isFile: () => !directory};
  },
  readdir: async p => (await exec(["find", p, "-mindepth", "1", "-maxdepth", "1", "-print0"])).toString().split("\0").filter(Boolean).map(p => path.posix.basename(p)),
 }}));
 const find = createFindToolDefinition(remoteCwd);
 pi.registerTool({...find, async execute(_id, params, signal) {
  const args = ["rg", "--files", "--hidden", "-g", "!.git", "-g", params.pattern, remotePath(params.path ?? ".")];
  const result = await run(["container", "exec", "-w", remoteCwd, target!, ...args], undefined, signal);
  if (result.code > 1) throw new Error(result.err.toString());
  const text = result.out.toString().split("\n").slice(0, params.limit ?? 1000).join("\n");
  return {content: [{type: "text", text: truncateHead(text).content || "No files found"}], details: undefined};
 }});
 const grep = createGrepToolDefinition(remoteCwd);
 pi.registerTool({...grep, async execute(_id, params, signal) {
  const args = ["rg", "--line-number", "--hidden", "--color=never", "-g", "!.git"];
  if (params.ignoreCase) args.push("-i"); if (params.literal) args.push("-F");
  if (params.glob) args.push("-g", params.glob); if (params.context) args.push("-C", String(params.context));
  args.push("--", params.pattern, remotePath(params.path ?? "."));
  const result = await run(["container", "exec", "-w", remoteCwd, target!, ...args], undefined, signal);
  if (result.code > 1) throw new Error(result.err.toString());
  const text = result.out.toString().split("\n").slice(0, params.limit ?? 100).join("\n");
  return {content: [{type: "text", text: truncateHead(text).content || "No matches found"}], details: undefined};
 }});
 pi.on("before_agent_start", async event => {
  await exec(["test", "-d", remoteCwd]);
  let instructions = "";
  for (const name of ["AGENTS.md", "CLAUDE.md"]) {
   const p = `${remoteCwd}/${name}`;
   if (await exists(p)) instructions += `\n\n# ${p}\n${(await read.readFile(p)).toString()}`;
  }
  return {systemPrompt: event.systemPrompt.replace(`Current working directory: ${process.cwd()}`, `Current working directory: ${remoteCwd}`) + (process.env.WI_EXECUTOR_NOTE ?? "") + instructions};
 });
}
