import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import { readFileSync, unlinkSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";

const SUPPORTED_IMAGE_MIME_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"] as const;
const DEFAULT_LIST_TIMEOUT_MS = 1000;
const DEFAULT_READ_TIMEOUT_MS = 3000;
const DEFAULT_POWERSHELL_TIMEOUT_MS = 5000;
const DEFAULT_MAX_BUFFER_BYTES = 50 * 1024 * 1024;

type ClipboardImage = {
	bytes: Uint8Array;
	mimeType: string;
};

type NativeClipboard = {
	hasImage(): boolean;
	getImageBinary(): Promise<Uint8Array | number[] | Buffer | null | undefined>;
};

let nativeClipboardLoaded = false;
let nativeClipboard: NativeClipboard | null = null;

export function isWaylandSession(env = process.env): boolean {
	return Boolean(env.WAYLAND_DISPLAY) || env.XDG_SESSION_TYPE === "wayland";
}

function baseMimeType(mimeType: string): string {
	return mimeType.split(";")[0]?.trim().toLowerCase() ?? mimeType.toLowerCase();
}

export function extensionForImageMimeType(mimeType: string): string | null {
	switch (baseMimeType(mimeType)) {
		case "image/png":
			return "png";
		case "image/jpeg":
			return "jpg";
		case "image/webp":
			return "webp";
		case "image/gif":
			return "gif";
		default:
			return null;
	}
}

function selectPreferredImageMimeType(mimeTypes: string[]): string | null {
	const normalized = mimeTypes
		.map((t) => t.trim())
		.filter(Boolean)
		.map((t) => ({ raw: t, base: baseMimeType(t) }));

	for (const preferred of SUPPORTED_IMAGE_MIME_TYPES) {
		const match = normalized.find((t) => t.base === preferred);
		if (match) return match.raw;
	}

	return null;
}

function isSupportedImageMimeType(mimeType: string): boolean {
	const base = baseMimeType(mimeType);
	return SUPPORTED_IMAGE_MIME_TYPES.some((t) => t === base);
}

function runCommand(
	command: string,
	args: string[],
	options: { timeoutMs?: number; maxBufferBytes?: number; env?: NodeJS.ProcessEnv } = {},
): { ok: true; stdout: Buffer } | { ok: false; stdout: Buffer } {
	const result = spawnSync(command, args, {
		timeout: options.timeoutMs ?? DEFAULT_READ_TIMEOUT_MS,
		maxBuffer: options.maxBufferBytes ?? DEFAULT_MAX_BUFFER_BYTES,
		env: options.env,
	});
	if (result.error || result.status !== 0) return { ok: false, stdout: Buffer.alloc(0) };
	const stdout = Buffer.isBuffer(result.stdout)
		? result.stdout
		: Buffer.from(result.stdout ?? "", typeof result.stdout === "string" ? "utf-8" : undefined);
	return { ok: true, stdout };
}

function readClipboardImageViaWlPaste(): ClipboardImage | null {
	const list = runCommand("wl-paste", ["--list-types"], { timeoutMs: DEFAULT_LIST_TIMEOUT_MS });
	if (!list.ok) return null;
	const types = list.stdout
		.toString("utf-8")
		.split(/\r?\n/)
		.map((t) => t.trim())
		.filter(Boolean);
	const selectedType = selectPreferredImageMimeType(types);
	if (!selectedType) return null;
	const data = runCommand("wl-paste", ["--type", selectedType, "--no-newline"]);
	if (!data.ok || data.stdout.length === 0) return null;
	return { bytes: data.stdout, mimeType: baseMimeType(selectedType) };
}

function readClipboardImageViaXclip(): ClipboardImage | null {
	const targets = runCommand("xclip", ["-selection", "clipboard", "-t", "TARGETS", "-o"], {
		timeoutMs: DEFAULT_LIST_TIMEOUT_MS,
	});
	let candidateTypes: string[] = [];
	if (targets.ok) {
		candidateTypes = targets.stdout
			.toString("utf-8")
			.split(/\r?\n/)
			.map((t) => t.trim())
			.filter(Boolean);
	}
	const preferred = candidateTypes.length > 0 ? selectPreferredImageMimeType(candidateTypes) : null;
	const tryTypes = preferred ? [preferred, ...SUPPORTED_IMAGE_MIME_TYPES] : [...SUPPORTED_IMAGE_MIME_TYPES];
	for (const mimeType of tryTypes) {
		const data = runCommand("xclip", ["-selection", "clipboard", "-t", mimeType, "-o"]);
		if (data.ok && data.stdout.length > 0) {
			return { bytes: data.stdout, mimeType: baseMimeType(mimeType) };
		}
	}
	return null;
}

function isWSL(env = process.env): boolean {
	if (env.WSL_DISTRO_NAME || env.WSLENV) return true;
	try {
		const release = readFileSync("/proc/version", "utf-8");
		return /microsoft|wsl/i.test(release);
	} catch {
		return false;
	}
}

function readClipboardImageViaPowerShell(): ClipboardImage | null {
	const tmpFile = path.join(tmpdir(), `pi-wsl-clip-${randomUUID()}.png`);
	try {
		const winPathResult = runCommand("wslpath", ["-w", tmpFile], { timeoutMs: DEFAULT_LIST_TIMEOUT_MS });
		if (!winPathResult.ok) return null;
		const winPath = winPathResult.stdout.toString("utf-8").trim();
		if (!winPath) return null;
		const psQuotedWinPath = winPath.replaceAll("'", "''");
		const psScript = [
			"Add-Type -AssemblyName System.Windows.Forms",
			"Add-Type -AssemblyName System.Drawing",
			`$path = '${psQuotedWinPath}'`,
			"$img = [System.Windows.Forms.Clipboard]::GetImage()",
			"if ($img) { $img.Save($path, [System.Drawing.Imaging.ImageFormat]::Png); Write-Output 'ok' } else { Write-Output 'empty' }",
		].join("; ");
		const result = runCommand("powershell.exe", ["-NoProfile", "-Command", psScript], {
			timeoutMs: DEFAULT_POWERSHELL_TIMEOUT_MS,
		});
		if (!result.ok || result.stdout.toString("utf-8").trim() !== "ok") return null;
		const bytes = readFileSync(tmpFile);
		if (bytes.length === 0) return null;
		return { bytes, mimeType: "image/png" };
	} catch {
		return null;
	} finally {
		try {
			unlinkSync(tmpFile);
		} catch {
			// Ignore cleanup errors.
		}
	}
}

function loadNativeClipboard(): NativeClipboard | null {
	if (nativeClipboardLoaded) return nativeClipboard;
	nativeClipboardLoaded = true;
	const hasDisplay = process.platform !== "linux" || Boolean(process.env.DISPLAY || process.env.WAYLAND_DISPLAY);
	if (process.env.TERMUX_VERSION || !hasDisplay) return null;

	const roots = [import.meta.url];
	if (process.argv[1] && path.isAbsolute(process.argv[1])) roots.push(process.argv[1]);
	for (const root of roots) {
		try {
			const req = createRequire(root);
			nativeClipboard = req("@mariozechner/clipboard") as NativeClipboard;
			return nativeClipboard;
		} catch {
			// Try the next resolution root.
		}
	}
	return null;
}

async function readClipboardImageViaNativeClipboard(): Promise<ClipboardImage | null> {
	const clipboard = loadNativeClipboard();
	if (!clipboard || !clipboard.hasImage()) return null;
	const imageData = await clipboard.getImageBinary();
	if (!imageData || imageData.length === 0) return null;
	const bytes = imageData instanceof Uint8Array ? imageData : Uint8Array.from(imageData);
	return { bytes, mimeType: "image/png" };
}

export async function readClipboardImage(options?: { env?: NodeJS.ProcessEnv; platform?: NodeJS.Platform }): Promise<ClipboardImage | null> {
	const env = options?.env ?? process.env;
	const platform = options?.platform ?? process.platform;
	if (env.TERMUX_VERSION) return null;

	let image: ClipboardImage | null = null;
	if (platform === "linux") {
		const wsl = isWSL(env);
		const wayland = isWaylandSession(env);
		if (wayland || wsl) image = readClipboardImageViaWlPaste() ?? readClipboardImageViaXclip();
		if (!image && wsl) image = readClipboardImageViaPowerShell();
		if (!image && !wayland) image = readClipboardImageViaXclip() ?? (await readClipboardImageViaNativeClipboard());
	} else {
		image = await readClipboardImageViaNativeClipboard();
	}

	if (!image || !isSupportedImageMimeType(image.mimeType)) return null;
	return image;
}
