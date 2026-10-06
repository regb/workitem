/**
 * Remote Exec Tool Routing
 *
 * Routes Pi's built-in coding tools through an exec-compatible backend while Pi itself remains on the host.
 *
 * Shared implementation for the generic container-exec extension, the
 * Agentbox convenience extension, and remote sandbox adapters. Load one of the sibling extension entrypoints
 * explicitly with --extension/-e; this core file is not intended to be loaded
 * directly.
 */

import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import path from "node:path";
import type { BeforeAgentStartEvent, ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import type { AutocompleteItem, AutocompleteSuggestions } from "@earendil-works/pi-tui";
import {
	type BashOperations,
	CustomEditor,
	createBashToolDefinition,
	createEditToolDefinition,
	createFindToolDefinition,
	createGrepToolDefinition,
	createLsToolDefinition,
	createReadToolDefinition,
	createWriteToolDefinition,
	DEFAULT_MAX_BYTES,
	type EditOperations,
	type FindOperations,
	formatSize,
	getAgentDir,
	type GrepToolDetails,
	type GrepToolInput,
	type LsOperations,
	type ReadOperations,
	truncateHead,
	truncateLine,
	type WriteOperations,
} from "@earendil-works/pi-coding-agent";
import { buildContainerBashWrapper, buildTerminateContainerBashScript } from "./bash-support";
import { extensionForImageMimeType, readClipboardImage } from "./clipboard-image-support";
import { buildFindScript, DEFAULT_FIND_LIMIT, normalizeFindOutput } from "./find-support";
import { buildGrepFallbackScript } from "./grep-support";
import { buildDirectoryListingScript } from "./ls-support";
import { isRecoverableContainerError } from "./recovery-support";
import { sanitizePositiveInteger, shellCommandFlag } from "./shell-support";

const DEFAULT_GREP_LIMIT = 100;
const CONTEXT_FILE_READ_TIMEOUT_MS = 10_000;
const CONTEXT_FILE_CANDIDATES = ["AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"] as const;
const SUPPORTED_IMAGE_MIME_TYPES = new Set(["image/jpeg", "image/png", "image/gif", "image/webp"]);

type ExecRuntime = string;
type ContainerContextFilesMode = "container" | "host" | "none";

type ContextFile = {
	path: string;
	content: string;
};

type ExecResult = {
	code: number | null;
	stdout: Buffer;
	stderr: Buffer;
};

type TextToolResult<TDetails> = {
	content: Array<{ type: "text"; text: string }>;
	details: TDetails | undefined;
};

class ContainerExecError extends Error {
	constructor(
		message: string,
		readonly result?: ExecResult,
	) {
		super(message);
		this.name = "ContainerExecError";
	}
}

function stripAtPrefix(value: string): string {
	return value.startsWith("@") ? value.slice(1) : value;
}

function resolveInCwd(cwd: string, inputPath: string): string {
	const trimmed = stripAtPrefix(inputPath.trim());
	if (!trimmed) return cwd;
	if (path.isAbsolute(trimmed)) return path.normalize(trimmed);
	return path.resolve(cwd, trimmed);
}

function isInsidePath(root: string, value: string): boolean {
	const relativePath = path.relative(root, value);
	return relativePath === "" || (!relativePath.startsWith("..") && !path.isAbsolute(relativePath));
}

function containerDisplayPath(containerCwd: string, filePath: string, rootIsDirectory: boolean, searchRoot: string): string {
	if (rootIsDirectory && isInsidePath(searchRoot, filePath)) {
		const relativePath = path.relative(searchRoot, filePath);
		return relativePath ? relativePath.split(path.sep).join(path.posix.sep) : ".";
	}
	if (isInsidePath(containerCwd, filePath)) {
		const relativePath = path.relative(containerCwd, filePath);
		return relativePath ? relativePath.split(path.sep).join(path.posix.sep) : ".";
	}
	return path.basename(filePath);
}

const PATH_DELIMITERS = new Set([" ", "\t", '"', "'", "="]);

function toDisplayPath(value: string): string {
	return value.split(path.sep).join(path.posix.sep);
}

function findLastDelimiter(text: string): number {
	for (let index = text.length - 1; index >= 0; index -= 1) {
		if (PATH_DELIMITERS.has(text[index] ?? "")) return index;
	}
	return -1;
}

function findUnclosedQuoteStart(text: string): number | null {
	let inQuotes = false;
	let quoteStart = -1;
	for (let index = 0; index < text.length; index += 1) {
		if (text[index] === '"') {
			inQuotes = !inQuotes;
			if (inQuotes) quoteStart = index;
		}
	}
	return inQuotes ? quoteStart : null;
}

function isTokenStart(text: string, index: number): boolean {
	return index === 0 || PATH_DELIMITERS.has(text[index - 1] ?? "");
}

function extractQuotedPrefix(text: string): string | null {
	const quoteStart = findUnclosedQuoteStart(text);
	if (quoteStart === null) return null;
	if (quoteStart > 0 && text[quoteStart - 1] === "@") {
		return isTokenStart(text, quoteStart - 1) ? text.slice(quoteStart - 1) : null;
	}
	return isTokenStart(text, quoteStart) ? text.slice(quoteStart) : null;
}

function extractContainerPathPrefix(text: string, forceExtract = false): string | null {
	const quotedPrefix = extractQuotedPrefix(text);
	if (quotedPrefix) return quotedPrefix;
	const lastDelimiterIndex = findLastDelimiter(text);
	const tokenStart = lastDelimiterIndex === -1 ? 0 : lastDelimiterIndex + 1;
	const token = text.slice(tokenStart);
	if (token.startsWith("@")) return token;
	if (forceExtract) return token;
	if (token.includes("/") || token.startsWith(".") || token.startsWith("~/")) return token;
	if (token === "" && text.endsWith(" ")) return token;
	return null;
}

function parseCompletionPrefix(prefix: string): { rawPrefix: string; isAtPrefix: boolean; isQuotedPrefix: boolean } {
	if (prefix.startsWith('@"')) return { rawPrefix: prefix.slice(2), isAtPrefix: true, isQuotedPrefix: true };
	if (prefix.startsWith('"')) return { rawPrefix: prefix.slice(1), isAtPrefix: false, isQuotedPrefix: true };
	if (prefix.startsWith("@")) return { rawPrefix: prefix.slice(1), isAtPrefix: true, isQuotedPrefix: false };
	return { rawPrefix: prefix, isAtPrefix: false, isQuotedPrefix: false };
}

function buildCompletionValue(value: string, options: { isAtPrefix: boolean; isQuotedPrefix: boolean }): string {
	const normalized = toDisplayPath(value);
	const needsQuotes = options.isQuotedPrefix || normalized.includes(" ");
	const at = options.isAtPrefix ? "@" : "";
	return needsQuotes ? `${at}"${normalized}"` : `${at}${normalized}`;
}

function splitCompletionPrefix(containerCwd: string, rawPrefix: string): { searchDir: string; searchPrefix: string; displayBase: string } {
	const expanded = rawPrefix === "~" || rawPrefix.startsWith("~/") ? rawPrefix.replace(/^~/, "/home/dev") : rawPrefix;
	const hasTrailingSlash = expanded.endsWith("/");
	if (expanded === "" || expanded === "./" || expanded === "../" || expanded === "/" || hasTrailingSlash) {
		const searchDir = resolveInCwd(containerCwd, expanded || ".");
		return { searchDir, searchPrefix: "", displayBase: rawPrefix || "" };
	}
	const dirPart = path.posix.dirname(toDisplayPath(expanded));
	const basePart = path.posix.basename(toDisplayPath(expanded));
	const searchDir = path.posix.isAbsolute(expanded) ? dirPart : resolveInCwd(containerCwd, dirPart);
	let displayBase = dirPart === "." ? "" : dirPart === "/" ? "/" : `${dirPart}/`;
	if (rawPrefix.startsWith("~/") && !displayBase.startsWith("~/")) displayBase = `~/${displayBase.replace(/^\/home\/dev\/?/, "")}`;
	return { searchDir, searchPrefix: basePart, displayBase };
}

function execFileCollect(
	command: string,
	args: string[],
	options: { stdin?: string | Buffer; signal?: AbortSignal; timeoutMs?: number; check?: boolean } = {},
): Promise<ExecResult> {
	return new Promise((resolve, reject) => {
		if (options.signal?.aborted) {
			reject(new Error("aborted"));
			return;
		}

		const child = spawn(command, args, { stdio: ["pipe", "pipe", "pipe"] });
		const stdout: Buffer[] = [];
		const stderr: Buffer[] = [];
		let settled = false;
		let timedOut = false;
		let killTimer: NodeJS.Timeout | undefined;

		const settle = (fn: () => void) => {
			if (settled) return;
			settled = true;
			if (timer) clearTimeout(timer);
			if (killTimer) clearTimeout(killTimer);
			options.signal?.removeEventListener("abort", onAbort);
			fn();
		};

		const stopChild = () => {
			if (child.killed) return;
			child.kill("SIGTERM");
			killTimer = setTimeout(() => {
				if (!child.killed) child.kill("SIGKILL");
			}, 1000);
		};

		const onAbort = () => {
			stopChild();
		};

		const timer =
			options.timeoutMs && options.timeoutMs > 0
				? setTimeout(() => {
					timedOut = true;
					stopChild();
				}, options.timeoutMs)
				: undefined;

		options.signal?.addEventListener("abort", onAbort, { once: true });

		child.stdout.on("data", (chunk: Buffer) => stdout.push(chunk));
		child.stderr.on("data", (chunk: Buffer) => stderr.push(chunk));
		child.on("error", (error) => settle(() => reject(error)));
		child.on("close", (code) => {
			const result = { code, stdout: Buffer.concat(stdout), stderr: Buffer.concat(stderr) };
			settle(() => {
				if (options.signal?.aborted) {
					reject(new Error("aborted"));
					return;
				}
				if (timedOut) {
					reject(new Error(`timeout:${Math.ceil((options.timeoutMs ?? 0) / 1000)}`));
					return;
				}
				if (options.check !== false && code !== 0) {
					const stderrText = result.stderr.toString("utf8").trim();
					reject(new ContainerExecError(stderrText || `${command} exited with code ${code}`, result));
					return;
				}
				resolve(result);
			});
		});

		if (options.stdin !== undefined) child.stdin.end(options.stdin);
		else child.stdin.end();
	});
}

function runtimeCommand(
	runtime: ExecRuntime,
	args: string[],
	options?: { stdin?: string | Buffer; signal?: AbortSignal; timeoutMs?: number; check?: boolean },
) {
	return execFileCollect(runtime, args, options);
}

async function containerExec(
	runtime: ExecRuntime,
	container: string,
	command: string[],
	options: { cwd?: string; stdin?: string | Buffer; signal?: AbortSignal; timeoutMs?: number; check?: boolean } = {},
): Promise<ExecResult> {
	const args = ["exec"];
	if (options.stdin !== undefined) args.push("-i");
	if (options.cwd) args.push("-w", options.cwd);
	args.push(container, ...command);
	return runtimeCommand(runtime, args, options);
}

async function containerExecOk(runtime: ExecRuntime, container: string, command: string[], options: { cwd?: string; signal?: AbortSignal } = {}) {
	const result = await containerExec(runtime, container, command, { ...options, check: false });
	return result.code === 0;
}

async function copyBufferIntoContainer(runtime: ExecRuntime, container: string, containerPath: string, content: Uint8Array | Buffer): Promise<void> {
	const script = `
target=$1
dir=\${target%/*}
[ "$dir" = "$target" ] && dir=.
mkdir -p "$dir" && cat > "$target"
`;
	await containerExec(runtime, container, ["sh", "-c", script, "sh", containerPath], { stdin: Buffer.from(content) });
}

function normalizeContainerDir(dir: string): string {
	return path.posix.resolve("/", dir);
}

async function loadContainerProjectContextFiles(runtime: ExecRuntime, container: string, containerCwd: string): Promise<ContextFile[]> {
	const script = `
dir=$1
while :; do
  for name in ${CONTEXT_FILE_CANDIDATES.join(" ")}; do
    if [ "$dir" = "/" ]; then file="/$name"; else file="$dir/$name"; fi
    [ -f "$file" ] || continue
    if encoded=$(base64 "$file" 2>/dev/null | tr -d '\n'); then
      printf '%s\t%s\n' "$file" "$encoded"
      break
    fi
  done
  [ "$dir" = "/" ] && break
  parent=$(dirname "$dir")
  [ "$parent" = "$dir" ] && break
  dir="$parent"
done
`;
	const result = await containerExec(runtime, container, ["sh", "-c", script, "sh", normalizeContainerDir(containerCwd)], {
		check: false,
		timeoutMs: CONTEXT_FILE_READ_TIMEOUT_MS,
	});
	if (result.code !== 0) return [];

	const contextFiles: ContextFile[] = [];
	for (const line of result.stdout.toString("utf8").split("\n")) {
		if (!line) continue;
		const tabIndex = line.indexOf("\t");
		if (tabIndex <= 0) continue;
		const filePath = line.slice(0, tabIndex);
		const encoded = line.slice(tabIndex + 1);
		contextFiles.unshift({ path: filePath, content: Buffer.from(encoded, "base64").toString("utf8") });
	}

	return dedupeContextFiles(contextFiles);
}

function getHostGlobalContextFiles(contextFiles: readonly ContextFile[] | undefined): ContextFile[] {
	const agentDir = path.resolve(getAgentDir());
	return (contextFiles ?? []).filter((contextFile) => path.dirname(path.resolve(contextFile.path)) === agentDir);
}

function dedupeContextFiles(contextFiles: ContextFile[]): ContextFile[] {
	const deduped: ContextFile[] = [];
	const seenPaths = new Set<string>();
	for (const contextFile of contextFiles) {
		if (seenPaths.has(contextFile.path)) continue;
		seenPaths.add(contextFile.path);
		deduped.push(contextFile);
	}
	return deduped;
}

function formatContextSection(contextFiles: readonly ContextFile[]): string {
	if (contextFiles.length === 0) return "";
	let section = "\n\n# Project Context\n\n";
	section += "Project-specific instructions and guidelines:\n\n";
	for (const { path: filePath, content } of contextFiles) {
		section += `## ${filePath}\n\n${content}\n\n`;
	}
	return section;
}

function findContextSectionEnd(systemPrompt: string, startIndex: number): number {
	const skillsIndex = systemPrompt.indexOf("\n\nThe following skills provide specialized instructions for specific tasks.", startIndex);
	const dateIndex = systemPrompt.indexOf("\nCurrent date: ", startIndex);
	if (skillsIndex === -1) return dateIndex === -1 ? systemPrompt.length : dateIndex;
	if (dateIndex === -1) return skillsIndex;
	return Math.min(skillsIndex, dateIndex);
}

function insertContextSection(systemPrompt: string, newSection: string): string {
	if (!newSection) return systemPrompt;
	const dateIndex = systemPrompt.lastIndexOf("\nCurrent date: ");
	const skillsIndex = systemPrompt.lastIndexOf("\n\nThe following skills provide specialized instructions for specific tasks.");
	const insertIndex = skillsIndex >= 0 && (dateIndex === -1 || skillsIndex < dateIndex) ? skillsIndex : dateIndex;
	if (insertIndex >= 0) {
		return `${systemPrompt.slice(0, insertIndex)}${newSection}${systemPrompt.slice(insertIndex)}`;
	}
	return `${systemPrompt}${newSection}`;
}

function replaceContextSection(systemPrompt: string, oldContextFiles: readonly ContextFile[], newContextFiles: readonly ContextFile[]): string {
	const oldSection = formatContextSection(oldContextFiles);
	const newSection = formatContextSection(newContextFiles);

	if (oldSection) {
		const oldSectionIndex = systemPrompt.indexOf(oldSection);
		if (oldSectionIndex >= 0) {
			return `${systemPrompt.slice(0, oldSectionIndex)}${newSection}${systemPrompt.slice(oldSectionIndex + oldSection.length)}`;
		}
	}

	const header = "\n\n# Project Context\n\nProject-specific instructions and guidelines:\n\n";
	const headerIndex = systemPrompt.indexOf(header);
	if (headerIndex >= 0) {
		const endIndex = findContextSectionEnd(systemPrompt, headerIndex + header.length);
		return `${systemPrompt.slice(0, headerIndex)}${newSection}${systemPrompt.slice(endIndex)}`;
	}

	return insertContextSection(systemPrompt, newSection);
}

function buildContextAdjustedSystemPrompt(
	systemPrompt: string,
	hostContextFiles: readonly ContextFile[] | undefined,
	mode: ContainerContextFilesMode,
	containerContextFiles: readonly ContextFile[] = [],
	hostCwd?: string,
	targetCwd?: string,
): string {
	let adjusted = systemPrompt;
	if (mode === "none") adjusted = replaceContextSection(adjusted, hostContextFiles ?? [], []);
	if (mode === "container") {
		const preservedHostContextFiles = getHostGlobalContextFiles(hostContextFiles);
		const replacementContextFiles = dedupeContextFiles([...preservedHostContextFiles, ...containerContextFiles]);
		adjusted = replaceContextSection(adjusted, hostContextFiles ?? [], replacementContextFiles);
	}
	if (hostCwd && targetCwd && hostCwd !== targetCwd) {
		adjusted = adjusted.replace(`Current working directory: ${hostCwd}`, `Current working directory: ${targetCwd}`);
	}
	return adjusted;
}

function createContainerReadOps(runtime: ExecRuntime, container: string): ReadOperations {
	return {
		readFile: async (filePath) => (await containerExec(runtime, container, ["cat", filePath])).stdout,
		access: async (filePath) => {
			await containerExec(runtime, container, ["test", "-r", filePath]);
		},
		detectImageMimeType: async (filePath) => {
			try {
				const result = await containerExec(runtime, container, ["file", "--mime-type", "-b", filePath]);
				const mimeType = result.stdout.toString("utf8").trim();
				return SUPPORTED_IMAGE_MIME_TYPES.has(mimeType) ? mimeType : null;
			} catch {
				return null;
			}
		},
	};
}

function createContainerWriteOps(runtime: ExecRuntime, container: string): WriteOperations {
	return {
		writeFile: async (filePath, content) => {
			await containerExec(runtime, container, ["sh", "-c", "cat > \"$1\"", "sh", filePath], { stdin: content });
		},
		mkdir: async (dirPath) => {
			await containerExec(runtime, container, ["mkdir", "-p", dirPath]);
		},
	};
}

function createContainerEditOps(runtime: ExecRuntime, container: string): EditOperations {
	const readOps = createContainerReadOps(runtime, container);
	const writeOps = createContainerWriteOps(runtime, container);
	return {
		readFile: readOps.readFile,
		writeFile: writeOps.writeFile,
		access: async (filePath) => {
			await containerExec(runtime, container, ["sh", "-c", "test -r \"$1\" && test -w \"$1\"", "sh", filePath]);
		},
	};
}

function createContainerLsOps(runtime: ExecRuntime, container: string): LsOperations {
	return {
		exists: async (filePath) => containerExecOk(runtime, container, ["test", "-e", filePath]),
		stat: async (filePath) => {
			if (!(await containerExecOk(runtime, container, ["test", "-e", filePath]))) {
				throw new Error(`Path not found: ${filePath}`);
			}
			const isDirectory = await containerExecOk(runtime, container, ["test", "-d", filePath]);
			return { isDirectory: () => isDirectory };
		},
		readdir: async (dirPath) => {
			const result = await containerExec(runtime, container, ["sh", "-c", buildDirectoryListingScript(false), "sh", dirPath]);
			const output = result.stdout.toString("utf8");
			return output ? output.replace(/\n$/, "").split("\n").filter(Boolean) : [];
		},
	};
}

function createContainerFindOps(runtime: ExecRuntime, container: string): FindOperations {
	return {
		exists: async (filePath) => containerExecOk(runtime, container, ["test", "-e", filePath]),
		glob: async (pattern, cwd, options) => {
			const limit = sanitizePositiveInteger(options.limit, DEFAULT_FIND_LIMIT);
			const result = await containerExec(runtime, container, ["sh", "-c", buildFindScript(), "sh", pattern, cwd, String(limit)], { check: false });
			if (result.code !== 0) {
				const stderr = result.stderr.toString("utf8").trim();
				throw new Error(stderr || `find fallback exited with code ${result.code}`);
			}
			return normalizeFindOutput(result.stdout.toString("utf8"), cwd);
		},
	};
}

async function terminateContainerBash(runtime: ExecRuntime, container: string, markerPath: string): Promise<void> {
	await containerExec(runtime, container, ["sh", "-c", buildTerminateContainerBashScript(), "sh", markerPath], { check: false, timeoutMs: 3000 }).catch(() => undefined);
}

function mapBashCwdToContainer(hostCwd: string, containerCwd: string, cwd: string): string {
	if (!path.isAbsolute(cwd)) return resolveInCwd(containerCwd, cwd);
	if (cwd === hostCwd) return containerCwd;
	if (isInsidePath(hostCwd, cwd)) {
		const relativePath = path.relative(hostCwd, cwd).split(path.sep).join(path.posix.sep);
		return path.posix.resolve(containerCwd, relativePath);
	}
	return path.posix.normalize(cwd);
}

function createContainerBashOps(
	runtime: ExecRuntime,
	container: string,
	hostCwd: string,
	containerCwd: string,
	shellPath: string,
	execArgs: readonly string[] = [],
): BashOperations {
	return {
		exec: (command, cwd, { onData, signal, timeout }) =>
			new Promise((resolve, reject) => {
				if (signal?.aborted) {
					reject(new Error("aborted"));
					return;
				}

				const effectiveCwd = mapBashCwdToContainer(hostCwd, containerCwd, cwd);
				const markerPath = `/tmp/pi-container-exec-${randomUUID()}.pid`;
				const wrappedCommand = buildContainerBashWrapper(command, shellPath, timeout, markerPath);
				const args = ["exec", ...execArgs, "-i", "-w", effectiveCwd, container, shellPath, shellCommandFlag(shellPath), wrappedCommand];
				const child = spawn(runtime, args, { stdio: ["ignore", "pipe", "pipe"] });
				let timedOut = false;
				let settled = false;
				let killTimer: NodeJS.Timeout | undefined;
				let forceHostKillTimer: NodeJS.Timeout | undefined;
				let cleanupPromise: Promise<void> | undefined;

				const cleanupRemote = () => {
					cleanupPromise ??= terminateContainerBash(runtime, container, markerPath);
					return cleanupPromise;
				};

				const settle = (fn: () => void | Promise<void>) => {
					if (settled) return;
					settled = true;
					if (timer) clearTimeout(timer);
					if (killTimer) clearTimeout(killTimer);
					if (forceHostKillTimer) clearTimeout(forceHostKillTimer);
					signal?.removeEventListener("abort", onAbort);
					void Promise.resolve(fn()).catch(reject);
				};

				const killHostExec = () => {
					if (child.killed) return;
					child.kill("SIGTERM");
					killTimer = setTimeout(() => {
						if (!child.killed) child.kill("SIGKILL");
					}, 1000);
				};

				const stopChild = () => {
					void cleanupRemote().finally(killHostExec);
					forceHostKillTimer ??= setTimeout(killHostExec, 3500);
				};

				const onAbort = () => stopChild();
				const timer =
					timeout && timeout > 0
						? setTimeout(() => {
							timedOut = true;
							stopChild();
						}, (timeout + 3) * 1000)
						: undefined;

				signal?.addEventListener("abort", onAbort, { once: true });
				child.stdout.on("data", onData);
				child.stderr.on("data", onData);
				child.on("error", (error) => settle(() => reject(error)));
				child.on("close", (code) =>
					settle(async () => {
						if (cleanupPromise) await cleanupPromise;
						if (signal?.aborted) reject(new Error("aborted"));
						else if (timedOut || (timeout && timeout > 0 && code === 124)) reject(new Error(`timeout:${timeout}`));
						else resolve({ exitCode: code });
					}),
				);
			}),
	};
}

async function getContainerPathSuggestions(
	runtime: ExecRuntime,
	container: string,
	containerCwd: string,
	prefix: string,
	signal?: AbortSignal,
): Promise<AutocompleteSuggestions | null> {
	const parsed = parseCompletionPrefix(prefix);
	const { searchDir, searchPrefix, displayBase } = splitCompletionPrefix(containerCwd, parsed.rawPrefix);
	const result = await containerExec(runtime, container, ["sh", "-c", buildDirectoryListingScript(true), "sh", searchDir], { signal, check: false });
	if (result.code !== 0) return null;
	const loweredPrefix = searchPrefix.toLowerCase();
	const items: AutocompleteItem[] = [];
	for (const line of result.stdout.toString("utf8").split("\n")) {
		if (!line) continue;
		const tab = line.lastIndexOf("\t");
		if (tab <= 0) continue;
		const name = line.slice(0, tab);
		const kind = line.slice(tab + 1);
		if (searchPrefix && !name.toLowerCase().startsWith(loweredPrefix)) continue;
		const isDirectory = kind === "d";
		const completedPath = `${displayBase}${name}${isDirectory ? "/" : ""}`;
		items.push({
			value: buildCompletionValue(completedPath, parsed),
			label: `${name}${isDirectory ? "/" : ""}`,
			description: toDisplayPath(path.posix.join(displayBase || ".", name)),
		});
	}
	items.sort((a, b) => {
		const aDir = a.label.endsWith("/");
		const bDir = b.label.endsWith("/");
		if (aDir && !bDir) return -1;
		if (!aDir && bDir) return 1;
		return a.label.localeCompare(b.label);
	});
	return items.length > 0 ? { items, prefix } : null;
}

async function executeContainerGrepFallback(
	runtime: ExecRuntime,
	container: string,
	searchPath: string,
	params: GrepToolInput,
	effectiveLimit: number,
	contextLines: number,
	signal?: AbortSignal,
): Promise<TextToolResult<GrepToolDetails>> {
	const result = await containerExec(
		runtime,
		container,
		[
			"sh",
			"-c",
			buildGrepFallbackScript(),
			"sh",
			params.pattern,
			searchPath,
			params.glob ?? "",
			params.ignoreCase ? "1" : "0",
			params.literal ? "1" : "0",
			String(contextLines),
			String(effectiveLimit),
		],
		{ signal, check: false },
	);
	if (result.code !== 0) {
		const stderr = result.stderr.toString("utf8").trim();
		throw new Error(stderr || `grep fallback exited with code ${result.code}`);
	}
	const rawOutput = result.stdout.toString("utf8").replace(/\s+$/, "");
	if (!rawOutput) return { content: [{ type: "text", text: "No matches found" }], details: undefined };

	const truncation = truncateHead(rawOutput, { maxLines: Number.MAX_SAFE_INTEGER });
	const details: GrepToolDetails = {};
	let output = truncation.content;
	const notices: string[] = [];
	if (rawOutput.split("\n").length >= effectiveLimit) {
		details.matchLimitReached = effectiveLimit;
		notices.push(`${effectiveLimit} matches/output-lines limit reached`);
	}
	if (truncation.truncated) {
		details.truncation = truncation;
		notices.push(`${formatSize(DEFAULT_MAX_BYTES)} limit reached`);
	}
	if (notices.length > 0) output += `\n\n[${notices.join(". ")}]`;
	return { content: [{ type: "text", text: output }], details: Object.keys(details).length > 0 ? details : undefined };
}

async function executeContainerGrep(
	runtime: ExecRuntime,
	container: string,
	containerCwd: string,
	params: GrepToolInput,
	signal?: AbortSignal,
): Promise<TextToolResult<GrepToolDetails>> {
	const searchPath = resolveInCwd(containerCwd, params.path ?? ".");
	if (!(await containerExecOk(runtime, container, ["test", "-e", searchPath], { signal }))) {
		throw new Error(`Path not found: ${searchPath}`);
	}
	const rootIsDirectory = await containerExecOk(runtime, container, ["test", "-d", searchPath], { signal });
	const effectiveLimit = sanitizePositiveInteger(params.limit, DEFAULT_GREP_LIMIT);
	const contextLines = sanitizePositiveInteger(params.context && params.context > 0 ? params.context : undefined, 0);
	const hasRipgrep = await containerExecOk(runtime, container, ["sh", "-c", "command -v rg >/dev/null 2>&1"], { signal });
	if (!hasRipgrep) {
		return executeContainerGrepFallback(runtime, container, searchPath, params, effectiveLimit, contextLines, signal);
	}

	const args = ["rg", "--json", "--line-number", "--color=never", "--hidden", "--max-count", String(effectiveLimit)];
	if (params.ignoreCase) args.push("--ignore-case");
	if (params.literal) args.push("--fixed-strings");
	if (params.glob) args.push("--glob", params.glob);
	args.push("--", params.pattern, searchPath);

	const result = await containerExec(runtime, container, args, { cwd: containerCwd, signal, check: false });
	if (result.code === 1) return { content: [{ type: "text", text: "No matches found" }], details: undefined };
	if (result.code !== 0) {
		const stderr = result.stderr.toString("utf8").trim();
		throw new Error(stderr || `ripgrep exited with code ${result.code}`);
	}

	const readOps = createContainerReadOps(runtime, container);
	const outputLines: string[] = [];
	const details: GrepToolDetails = {};
	let matchCount = 0;
	let matchLimitReached = false;
	let linesTruncated = false;
	const fileCache = new Map<string, string[]>();

	const getLines = async (filePath: string) => {
		const absolutePath = path.isAbsolute(filePath) ? filePath : resolveInCwd(containerCwd, filePath);
		let lines = fileCache.get(absolutePath);
		if (!lines) {
			const content = (await readOps.readFile(absolutePath)).toString("utf8");
			lines = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
			fileCache.set(absolutePath, lines);
		}
		return { absolutePath, lines };
	};

	for (const line of result.stdout.toString("utf8").split("\n")) {
		if (signal?.aborted) throw new Error("Operation aborted");
		if (!line.trim() || matchCount >= effectiveLimit) continue;
		let event: any;
		try {
			event = JSON.parse(line);
		} catch {
			continue;
		}
		if (event.type !== "match") continue;
		const filePath = event.data?.path?.text;
		const lineNumber = event.data?.line_number;
		const lineText = event.data?.lines?.text;
		if (!filePath || typeof lineNumber !== "number") continue;

		matchCount++;
		const { absolutePath, lines } = await getLines(filePath);
		const displayPath = containerDisplayPath(containerCwd, absolutePath, rootIsDirectory, searchPath);
		const matchIndex = lineNumber - 1;
		const start = contextLines > 0 ? Math.max(0, matchIndex - contextLines) : matchIndex;
		const end = contextLines > 0 ? Math.min(lines.length - 1, matchIndex + contextLines) : matchIndex;

		for (let index = start; index <= end; index++) {
			const rawLine = contextLines === 0 && index === matchIndex && typeof lineText === "string" ? lineText.replace(/\n$/, "") : (lines[index] ?? "");
			const { text, wasTruncated } = truncateLine(rawLine.replace(/\r/g, ""));
			if (wasTruncated) linesTruncated = true;
			const separator = index === matchIndex ? ":" : "-";
			outputLines.push(`${displayPath}${separator}${index + 1}${separator} ${text}`);
		}

		if (matchCount >= effectiveLimit) {
			matchLimitReached = true;
			break;
		}
	}

	if (matchCount === 0) return { content: [{ type: "text", text: "No matches found" }], details: undefined };

	const truncation = truncateHead(outputLines.join("\n"), { maxLines: Number.MAX_SAFE_INTEGER });
	const notices: string[] = [];
	let output = truncation.content;
	if (matchLimitReached) {
		details.matchLimitReached = effectiveLimit;
		notices.push(`${effectiveLimit} matches limit reached`);
	}
	if (linesTruncated) {
		details.linesTruncated = true;
		notices.push("long lines truncated");
	}
	if (truncation.truncated) {
		details.truncation = truncation;
		notices.push(`${formatSize(DEFAULT_MAX_BYTES)} limit reached`);
	}
	if (notices.length > 0) output += `\n\n[${notices.join(". ")}]`;

	return {
		content: [{ type: "text", text: output }],
		details: Object.keys(details).length > 0 ? details : undefined,
	};
}

export type { ExecRuntime, ContainerContextFilesMode };

export interface RemoteExecTarget {
	runtime: ExecRuntime;
	container: string;
	cwd?: string;
	statusLabel?: string;
	bashExecArgs?: string[];
}

export interface RegisterRemoteExecToolsOptions {
	resolveTarget: (ctx?: ExtensionContext) => RemoteExecTarget | Promise<RemoteExecTarget>;
	localCwd?: string;
	prepareTarget?: (event: BeforeAgentStartEvent) => boolean | void | Promise<boolean | void>;
	prepareTargetForInteraction?: (ctx: ExtensionContext) => boolean | void | Promise<boolean | void>;
	statusKey?: string;
	commandName?: string;
	commandDescription?: string;
	defaultContextFilesMode?: ContainerContextFilesMode;
	contextFilesFlagName?: string;
	isEnabled?: () => boolean;
	disabledMessage?: string;
	targetLabel?: string;
}

export function parseContainerRuntime(raw: string | undefined, fallback: ExecRuntime = "podman"): ExecRuntime {
	const value = (raw || fallback).trim();
	if (value === "podman" || value === "docker") return value;
	throw new Error(`Unsupported container runtime '${value}'. Use 'podman' or 'docker'.`);
}

export function parseContainerContextFilesMode(raw: string | undefined, fallback: ContainerContextFilesMode = "container"): ContainerContextFilesMode {
	const value = (raw || fallback).trim();
	if (value === "container" || value === "host" || value === "none") return value;
	throw new Error(`Unsupported container context mode '${value}'. Use 'container', 'host', or 'none'.`);
}

function piNoContextFilesRequested(): boolean {
	return process.argv.some((arg) => arg === "--no-context-files" || arg === "-nc");
}

export function registerRemoteExecTools(pi: ExtensionAPI, options: RegisterRemoteExecToolsOptions) {
	const localCwd = path.resolve(options.localCwd ?? process.cwd());
	const localRead = createReadToolDefinition(localCwd);
	const localWrite = createWriteToolDefinition(localCwd);
	const localEdit = createEditToolDefinition(localCwd);
	const localBash = createBashToolDefinition(localCwd);
	const localLs = createLsToolDefinition(localCwd);
	const localFind = createFindToolDefinition(localCwd);
	const localGrep = createGrepToolDefinition(localCwd);
	const statusKey = options.statusKey ?? "container";
	const commandName = options.commandName ?? "container";
	const isEnabled = () => options.isEnabled?.() ?? true;
	const disabledMessage = options.disabledMessage ?? "Container tool routing is disabled.";
	const contextFilesFlagName = options.contextFilesFlagName ?? "container-context-files";
	const targetLabel = options.targetLabel ?? "Container";

	pi.registerFlag(contextFilesFlagName, {
		description:
			"AGENTS.md/CLAUDE.md source for the model prompt: container, host, or none (default: container; respects --no-context-files)",
		type: "string",
	});

	function getContextFilesMode(): ContainerContextFilesMode {
		const fallback = piNoContextFilesRequested() ? "none" : options.defaultContextFilesMode ?? "container";
		return parseContainerContextFilesMode(
			(pi.getFlag(contextFilesFlagName) as string | undefined) || process.env.PI_CONTAINER_CONTEXT_FILES,
			fallback,
		);
	}

	let target: RemoteExecTarget | undefined;
	let shellPath = "/bin/bash";
	let validated = false;
	let validation: Promise<void> | undefined;
	let interactionTargetError: string | undefined;
	let containerContextCache:
		| {
				runtime: ExecRuntime;
				container: string;
				cwd: string;
				files: ContextFile[];
			}
		| undefined;
	let containerContextLoad:
		| {
				runtime: ExecRuntime;
				container: string;
				cwd: string;
				promise: Promise<ContextFile[]>;
			}
		| undefined;

	function isSameContextTarget<T extends { runtime: ExecRuntime; container: string; cwd: string }>(
		entry: T | undefined,
		active: Required<Pick<RemoteExecTarget, "runtime" | "container" | "cwd">>,
	): entry is T {
		return !!entry && entry.runtime === active.runtime && entry.container === active.container && entry.cwd === active.cwd;
	}

	async function getContainerContextFiles(active: Required<Pick<RemoteExecTarget, "runtime" | "container" | "cwd">>): Promise<ContextFile[]> {
		if (isSameContextTarget(containerContextCache, active)) return containerContextCache.files;
		if (isSameContextTarget(containerContextLoad, active)) return containerContextLoad.promise;

		const runtime = active.runtime;
		const container = active.container;
		const cwd = active.cwd;
		const promise = loadContainerProjectContextFiles(runtime, container, cwd)
			.then((files) => {
				containerContextCache = { runtime, container, cwd, files };
				return files;
			})
			.finally(() => {
				if (containerContextLoad?.promise === promise) containerContextLoad = undefined;
			});
		containerContextLoad = { runtime, container, cwd, promise };
		return promise;
	}

	function invalidateTarget() {
		validated = false;
		target = undefined;
		containerContextCache = undefined;
		containerContextLoad = undefined;
	}

	async function withRecovery<T>(ctx: ExtensionContext | undefined, run: () => Promise<T>): Promise<T> {
		try {
			return await run();
		} catch (error) {
			if (!isRecoverableContainerError(error)) throw error;
			invalidateTarget();
			ctx?.ui.notify("Container disappeared or stopped; revalidating target and retrying once.", "warning");
			await ensureTarget(ctx);
			return run();
		}
	}

	async function validateTarget(ctx?: ExtensionContext): Promise<void> {
		const resolved = await options.resolveTarget(ctx);
		const containerCwd = resolved.cwd ?? localCwd;

		const exists = await runtimeCommand(resolved.runtime, ["container", "inspect", resolved.container], { check: false });
		if (exists.code !== 0) throw new Error(`${targetLabel} '${resolved.container}' does not exist or is unavailable.`);

		const running = await runtimeCommand(resolved.runtime, ["container", "inspect", "-f", "{{.State.Running}}", resolved.container]);
		if (running.stdout.toString("utf8").trim() !== "true") {
			throw new Error(`${targetLabel} '${resolved.container}' is not running.`);
		}

		if (!(await containerExecOk(resolved.runtime, resolved.container, ["test", "-d", containerCwd]))) {
			throw new Error(
				`Host cwd '${localCwd}' is not available as '${containerCwd}' in ${targetLabel.toLowerCase()} '${resolved.container}'. ` +
					"Configure the target cwd or its filesystem mapping.",
			);
		}

		const shellProbe = await containerExec(resolved.runtime, resolved.container, ["sh", "-c", "command -v bash || command -v sh"]);
		shellPath = shellProbe.stdout.toString("utf8").trim() || "/bin/sh";
		target = { ...resolved, cwd: containerCwd };
		validated = true;
		interactionTargetError = undefined;
		ctx?.ui.setStatus(statusKey, ctx.ui.theme.fg("accent", resolved.statusLabel ?? `${resolved.runtime}: ${resolved.container}`));
		ctx?.ui.notify(`${targetLabel} tool routing enabled: ${resolved.container} (${containerCwd})`, "info");
	}

	async function prepareTargetForInteraction(ctx: ExtensionContext): Promise<void> {
		if (!options.prepareTargetForInteraction) return;
		const changed = await options.prepareTargetForInteraction(ctx);
		if (changed !== false) {
			interactionTargetError = undefined;
			invalidateTarget();
		}
	}

	function targetErrorMessage(error: unknown): string {
		return error instanceof Error ? error.message : String(error);
	}

	function reportInteractionTargetError(ctx: ExtensionContext, error: unknown): void {
		const message = targetErrorMessage(error);
		if (message === interactionTargetError) return;
		interactionTargetError = message;
		ctx.ui.notify(`${targetLabel} setup failed:\n${message}`, "error");
	}

	async function ensureTarget(ctx?: ExtensionContext): Promise<Required<Pick<RemoteExecTarget, "runtime" | "container" | "cwd">> & RemoteExecTarget> {
		if (!isEnabled()) throw new Error(disabledMessage);
		if (validated && target?.cwd) return target as Required<Pick<RemoteExecTarget, "runtime" | "container" | "cwd">> & RemoteExecTarget;
		if (!validation) {
			validation = validateTarget(ctx).finally(() => {
				validation = undefined;
			});
		}
		await validation;
		if (!target?.cwd) throw new Error("container validation failed before resolving target.");
		return target as Required<Pick<RemoteExecTarget, "runtime" | "container" | "cwd">> & RemoteExecTarget;
	}

	function installContainerClipboardPasteHandler(ctx: ExtensionContext): void {
		const previousFactory = ctx.ui.getEditorComponent?.();
		ctx.ui.setEditorComponent((tui, editorTheme, keybindings) => {
			const editor = previousFactory ? previousFactory(tui, editorTheme, keybindings) : new CustomEditor(tui, editorTheme, keybindings);
			(editor as typeof editor & { onPasteImage?: () => void }).onPasteImage = () => {
				void (async () => {
					const image = await readClipboardImage();
					if (!image) return;
					const ext = extensionForImageMimeType(image.mimeType) ?? "png";
					const containerPath = `/tmp/pi-clipboard-${randomUUID()}.${ext}`;
					try {
						await withRecovery(ctx, async () => {
							const active = await ensureTarget(ctx);
							await copyBufferIntoContainer(active.runtime, active.container, containerPath, image.bytes);
						});
						ctx.ui.pasteToEditor(containerPath);
					} catch (error) {
						const message = error instanceof Error ? error.message : String(error);
						ctx.ui.notify(`Image paste failed: ${message}`, "warning");
					}
				})().catch((error) => {
					const message = error instanceof Error ? error.message : String(error);
					ctx.ui.notify(`Image paste failed: ${message}`, "warning");
				});
			};
			return editor;
		});
	}

	pi.on("session_start", async (_event, ctx) => {
		if (!isEnabled()) return;
		registerToolOverrides();
		if (!options.prepareTarget) {
			const active = await ensureTarget(ctx);
			if (getContextFilesMode() === "container") {
				void getContainerContextFiles(active).catch(() => {
					// before_agent_start retries and reports persistent context errors.
				});
			}
		}
		if (ctx.hasUI) installContainerClipboardPasteHandler(ctx);
		ctx.ui.addAutocompleteProvider((current) => ({
			async getSuggestions(lines, cursorLine, cursorCol, autocompleteOptions) {
				const currentLine = lines[cursorLine] || "";
				const textBeforeCursor = currentLine.slice(0, cursorCol);
				const slashCommandContext =
					!autocompleteOptions.force &&
					textBeforeCursor.startsWith("/") &&
					!textBeforeCursor.includes(" ") &&
					!textBeforeCursor.slice(1).includes("/");
				if (slashCommandContext) return current.getSuggestions(lines, cursorLine, cursorCol, autocompleteOptions);

				const prefix = extractContainerPathPrefix(textBeforeCursor, autocompleteOptions.force ?? false);
				if (prefix !== null) {
					let active: Required<Pick<RemoteExecTarget, "runtime" | "container" | "cwd">> & RemoteExecTarget;
					try {
						await prepareTargetForInteraction(ctx);
						if (interactionTargetError) return null;
						active = await ensureTarget(ctx);
					} catch (error) {
						reportInteractionTargetError(ctx, error);
						return null;
					}
					try {
						return await getContainerPathSuggestions(active.runtime, active.container, active.cwd, prefix, autocompleteOptions.signal);
					} catch {
						return null;
					}
				}

				return current.getSuggestions(lines, cursorLine, cursorCol, autocompleteOptions);
			},
			applyCompletion(lines, cursorLine, cursorCol, item, prefix) {
				return current.applyCompletion(lines, cursorLine, cursorCol, item, prefix);
			},
			shouldTriggerFileCompletion(lines, cursorLine, cursorCol) {
				const currentLine = lines[cursorLine] || "";
				const textBeforeCursor = currentLine.slice(0, cursorCol);
				if (extractContainerPathPrefix(textBeforeCursor, false) !== null) return true;
				return current.shouldTriggerFileCompletion?.(lines, cursorLine, cursorCol) ?? true;
			},
		}));
	});

	pi.on("session_shutdown", async (_event, ctx) => {
		containerContextCache = undefined;
		containerContextLoad = undefined;
		ctx.ui.setStatus(statusKey, undefined);
	});

	pi.registerCommand(commandName, {
		description: options.commandDescription ?? "Show container tool-routing status",
		handler: async (_args, ctx) => {
			if (!isEnabled()) {
				ctx.ui.notify(disabledMessage, "info");
				return;
			}
			const active = await ensureTarget(ctx);
			ctx.ui.notify(
				[
					`Backend: ${active.runtime}`,
					`Target: ${active.container}`,
					`Host cwd: ${localCwd}`,
					`Container cwd: ${active.cwd}`,
					`Shell: ${shellPath}`,
					`Context files: ${getContextFilesMode()}`,
				].join("\n"),
				"info",
			);
		},
	});

	let toolOverridesRegistered = false;
	function registerToolOverrides(): void {
		if (toolOverridesRegistered) return;
		toolOverridesRegistered = true;

		pi.registerTool({
			...localRead,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localRead.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return createReadToolDefinition(active.cwd, { operations: createContainerReadOps(active.runtime, active.container) }).execute(
						id,
						params,
						signal,
						onUpdate,
						ctx,
					);
				});
			},
		});

		pi.registerTool({
			...localWrite,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localWrite.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return createWriteToolDefinition(active.cwd, { operations: createContainerWriteOps(active.runtime, active.container) }).execute(
						id,
						params,
						signal,
						onUpdate,
						ctx,
					);
				});
			},
		});

		pi.registerTool({
			...localEdit,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localEdit.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return createEditToolDefinition(active.cwd, { operations: createContainerEditOps(active.runtime, active.container) }).execute(
						id,
						params,
						signal,
						onUpdate,
						ctx,
					);
				});
			},
		});

		pi.registerTool({
			...localBash,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localBash.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return createBashToolDefinition(active.cwd, {
						operations: createContainerBashOps(active.runtime, active.container, localCwd, active.cwd, shellPath, active.bashExecArgs),
					}).execute(id, params, signal, onUpdate, ctx);
				});
			},
		});

		pi.registerTool({
			...localLs,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localLs.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return createLsToolDefinition(active.cwd, { operations: createContainerLsOps(active.runtime, active.container) }).execute(
						id,
						params,
						signal,
						onUpdate,
						ctx,
					);
				});
			},
		});

		pi.registerTool({
			...localFind,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localFind.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return createFindToolDefinition(active.cwd, { operations: createContainerFindOps(active.runtime, active.container) }).execute(
						id,
						params,
						signal,
						onUpdate,
						ctx,
					);
				});
			},
		});

		pi.registerTool({
			...localGrep,
			async execute(id, params, signal, onUpdate, ctx) {
				if (!isEnabled()) return localGrep.execute(id, params, signal, onUpdate, ctx);
				return withRecovery(ctx, async () => {
					const active = await ensureTarget(ctx);
					return executeContainerGrep(active.runtime, active.container, active.cwd, params, signal);
				});
			},
		});
	}

	pi.on("input", async (_event, ctx) => {
		if (!isEnabled() || !options.prepareTargetForInteraction) return undefined;
		interactionTargetError = undefined;
		try {
			await prepareTargetForInteraction(ctx);
			await ensureTarget(ctx);
			return undefined;
		} catch (error) {
			reportInteractionTargetError(ctx, error);
			return { action: "handled" as const };
		}
	});

	pi.on("user_bash", async (_event, ctx) => {
		if (!isEnabled()) return undefined;
		return {
			operations: {
				exec: (command, cwd, execOptions) =>
					withRecovery(ctx, async () => {
						// Interactive !/!! commands bypass Pi's normal input and
						// before_agent_start events. Prepare lazy targets here too so
						// the first command can create an Agentbox after discovery.
						interactionTargetError = undefined;
						await prepareTargetForInteraction(ctx);
						const active = await ensureTarget(ctx);
						return createContainerBashOps(active.runtime, active.container, localCwd, active.cwd, shellPath, active.bashExecArgs).exec(command, cwd, execOptions);
					}),
			},
		};
	});

	pi.on("before_agent_start", async (event, ctx) => {
		if (!isEnabled()) return undefined;
		// Pi has finished resource discovery by this point. Agentbox can now derive
		// exact read-only mounts from the skills and documentation Pi advertised.
		if (options.prepareTarget) {
			const changed = await options.prepareTarget(event);
			if (changed !== false) invalidateTarget();
		}
		// Validate before the first turn, but do not add container-routing details to
		// the model prompt. The overridden tools should look like normal Pi tools;
		// the sandbox boundary is an execution detail handled by the extension.
		const active = await ensureTarget(ctx);
		const contextFilesMode = getContextFilesMode();
		const containerContextFiles = contextFilesMode === "container" ? await getContainerContextFiles(active) : [];
		const systemPrompt = buildContextAdjustedSystemPrompt(
			event.systemPrompt,
			event.systemPromptOptions.contextFiles,
			contextFilesMode,
			containerContextFiles,
			localCwd,
			active.cwd,
		);
		return systemPrompt === event.systemPrompt ? undefined : { systemPrompt };
	});
}
