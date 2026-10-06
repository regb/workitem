import { shellCommandFlag, shellQuote } from "./shell-support";

export function buildContainerBashWrapper(command: string, shellPath: string, timeout: number | undefined, markerPath: string): string {
	const timeoutSecs = timeout && timeout > 0 ? Math.max(1, Math.ceil(timeout)) : undefined;
	const commandRunner = `exec ${shellQuote(shellPath)} ${shellCommandFlag(shellPath)} ${shellQuote(command)}`;
	const timeoutScript = timeoutSecs
		? `
_timeout_marker="$_marker.timeout"
(
  sleep ${timeoutSecs}
  printf '1\\n' > "$_timeout_marker"
  _target=$(cat "$_marker" 2>/dev/null || true)
  if [ -n "$_target" ]; then
    kill -TERM "$_target" 2>/dev/null || true
    sleep 1
    kill -KILL "$_target" 2>/dev/null || true
  fi
) &
_watchdog=$!
`
		: `
_timeout_marker=
_watchdog=
`;
	return `
set +e
_marker=${shellQuote(markerPath)}
_runner=${shellQuote(commandRunner)}
if command -v setsid >/dev/null 2>&1; then
  setsid sh -c "$_runner" &
  _pid=$!
  printf -- '-%s\\n' "$_pid" > "$_marker"
else
  sh -c "$_runner" &
  _pid=$!
  printf '%s\\n' "$_pid" > "$_marker"
fi
${timeoutScript}
wait "$_pid"
_rc=$?
if [ -n "$_watchdog" ]; then
  kill "$_watchdog" 2>/dev/null || true
  wait "$_watchdog" 2>/dev/null || true
fi
if [ -n "$_timeout_marker" ] && [ -f "$_timeout_marker" ]; then
  _rc=124
  rm -f "$_timeout_marker"
fi
rm -f "$_marker"
exit "$_rc"
`;
}

export function buildTerminateContainerBashScript(): string {
	return `
marker=$1
target=
for _attempt in 1 2 3 4 5 6 7 8 9 10; do
  target=$(cat "$marker" 2>/dev/null || true)
  [ -n "$target" ] && break
  sleep 0.1
done
if [ -n "$target" ]; then
  kill -TERM "$target" 2>/dev/null || true
  sleep 0.2
  kill -KILL "$target" 2>/dev/null || true
fi
rm -f "$marker" 2>/dev/null || true
`;
}
