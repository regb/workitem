import path from "node:path";

export function shellQuote(value: string): string {
	return `'${value.replace(/'/g, `'\\''`)}'`;
}

export function shellCommandFlag(shellPath: string): "-lc" | "-c" {
	const shellName = path.posix.basename(shellPath);
	return shellName === "bash" || shellName === "zsh" || shellName === "ksh" ? "-lc" : "-c";
}

export function sanitizePositiveInteger(value: number | undefined, fallback: number): number {
	return typeof value === "number" && Number.isFinite(value) ? Math.max(1, Math.floor(value)) : fallback;
}
