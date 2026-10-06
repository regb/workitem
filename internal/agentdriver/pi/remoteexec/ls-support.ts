export function buildDirectoryListingScript(includeKind = false): string {
	const kindLine = includeKind ? "  if [ -d \"$p\" ]; then t=d; else t=f; fi\n  printf '%s\\t%s\\n' \"$b\" \"$t\"" : "  printf '%s\\n' \"$b\"";
	return `
for p in "$1"/* "$1"/.[!.]* "$1"/..?*; do
  [ -e "$p" ] || continue
  b=\${p##*/}
  [ "$b" = "." ] && continue
  [ "$b" = ".." ] && continue
${kindLine}
done
`;
}
