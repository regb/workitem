export function buildGrepFallbackScript(): string {
	return `
set +e
pattern=$1
root=$2
glob=$3
ignore_case=$4
literal=$5
context=$6
limit=$7
if ! command -v grep >/dev/null 2>&1; then
  echo "grep is not installed" >&2
  exit 127
fi
grep_args="-nI"
[ "$ignore_case" = "1" ] && grep_args="$grep_args -i"
[ "$literal" = "1" ] && grep_args="$grep_args -F"
[ "$context" -gt 0 ] 2>/dev/null && grep_args="$grep_args -C $context"
if [ -d "$root" ]; then
  if [ -n "$glob" ]; then
    case "$glob" in
      */*) find "$root" -type f -path "*/$glob" ! -path '*/.git/*' ! -path '*/node_modules/*' -exec grep $grep_args -e "$pattern" /dev/null {} + 2>/dev/null | head -n "$limit" ;;
      *) find "$root" -type f -name "$glob" ! -path '*/.git/*' ! -path '*/node_modules/*' -exec grep $grep_args -e "$pattern" /dev/null {} + 2>/dev/null | head -n "$limit" ;;
    esac
  else
    grep -r $grep_args --exclude-dir=.git --exclude-dir=node_modules -e "$pattern" "$root" 2>/dev/null | head -n "$limit"
  fi
else
  grep $grep_args -e "$pattern" "$root" 2>/dev/null | head -n "$limit"
fi
exit 0
`;
}
