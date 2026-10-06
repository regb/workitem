export function isRecoverableContainerError(error: unknown): boolean {
	const message = error instanceof Error ? error.message : String(error);
	return /no such (?:container|sandbox)|does not exist|not running|is not running|(?:container|sandbox) state improper|(?:container|sandbox) .* stopped|cannot exec/i.test(message);
}
