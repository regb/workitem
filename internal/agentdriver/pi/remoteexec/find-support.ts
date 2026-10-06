import path from "node:path";

export const DEFAULT_FIND_LIMIT = 1000;

export function buildFindScript(): string {
	return `
set +e
pattern=$1
root=$2
limit=$3
cd "$root" || exit 3
if command -v fd >/dev/null 2>&1; then
  fd --glob --color=never --hidden --no-require-git --exclude .git --exclude node_modules --max-results "$limit" -- "$pattern" .
  rc=$?
  case "$rc" in 0|1) exit 0 ;; *) exit "$rc" ;; esac
fi
if command -v rg >/dev/null 2>&1; then
  effective=$pattern
  case "$effective" in
    */*) case "$effective" in /*|**/*) ;; *) effective="**/$effective" ;; esac ;;
  esac
  rg --files --hidden -g '!**/.git/**' -g '!**/node_modules/**' -g "$effective" 2>/dev/null | head -n "$limit"
  exit 0
fi
basename=\${pattern##*/}
find . -type f -name "$basename" ! -path '*/.git/*' ! -path '*/node_modules/*' 2>/dev/null | head -n "$limit"
exit 0
`;
}

export function normalizeFindOutput(stdout: string, cwd: string): string[] {
	return stdout
		.split("\n")
		.map((line) => line.replace(/\r$/, ""))
		.filter((line) => line.length > 0)
		.map((line) => {
			const normalized = line.replace(/^\.\//, "");
			return path.posix.isAbsolute(normalized) ? normalized : path.posix.join(cwd, normalized);
		});
}
