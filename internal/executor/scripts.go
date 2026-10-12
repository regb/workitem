package executor

// Scripts are text shipped by the host, never an installed executor-side daemon.
// Docker v1 requires coreutils timeout, which supplies a separate process group
// for each job and enforces its deadline even while the host is disconnected.
const operationScript = `
set -eu
owner=$1 claim=$2 generation=$3 op=$4
work=$5 state=$6
shift 6
socket=$state/tmux.sock
fail() { printf 'wi-executor: %s\n' "$*" >&2; exit 125; }
locked=
unlock() { if [ -n "$locked" ]; then rmdir "$state/lock"; locked=; fi; }
trap unlock EXIT
trap 'exit 125' HUP INT TERM
lock() {
 n=0
 until mkdir "$state/lock" 2>/dev/null; do
  n=$((n+1)); [ "$n" -lt 200 ] || fail 'executor is busy; retry after the active operation ends'
  sleep 0.05
 done
 locked=1
}
check() {
 [ "$(cat "$state/owner")" = "$owner" ] || fail 'owner mismatch'
 [ "$(cat "$state/claim")" = "$claim $generation" ] || fail 'stale claim'
}
ready() { [ ! -e "$state/releasing" ] || fail 'claim is releasing'; }
cwd() {
 cd "$1" || fail 'working directory does not exist'
 resolved=$(pwd -P)
 case "$resolved" in "$work"|"$work"/*) ;; *) fail 'working directory escapes workspace';; esac
}
mux() { tmux -S "$socket" -f "$state/tmux.conf" "$@"; }
cancel() {
 dir=$1
 [ ! -e "$dir/exit" ] || return 0
 if [ -f "$dir/pid" ] && mux has-session -t "job-${dir##*/}" 2>/dev/null; then
  pid=$(cat "$dir/pid")
  case "$pid" in ''|*[!0-9]*) fail 'invalid job process group';; esac
  kill -TERM -- "-$pid" 2>/dev/null || true
  sleep 0.2
  kill -KILL -- "-$pid" 2>/dev/null || true
 fi
 printf '130\n' > "$dir/exit.tmp"; mv "$dir/exit.tmp" "$dir/exit"
}
stop_jobs() {
 for dir in "$state"/jobs/*; do [ -d "$dir" ] || continue; cancel "$dir"; done
}
if [ "$op" = init ]; then
 for tool in sh bash git tmux timeout cat mkdir mv rm tail head wc file find sort grep sed rg sha256sum mktemp chmod dirname touch sleep uname ls; do command -v "$tool" >/dev/null || fail "missing required tool: $tool"; done
 if [ "$state" != /wi ]; then
  parent=$(dirname "$state"); root=$(dirname "$parent")
  mkdir -p "$root"
  [ "$(cd "$root" && pwd -P)" = "$root" ] || fail 'SSH root must not contain symlinks'
  [ ! -L "$parent" ] && [ ! -L "$state" ] && [ ! -L "$work" ] || fail 'symlink in owned workspace'
  mkdir -p "$state" "$work"
  chmod 700 "$parent" "$state" "$work"
 else
  mkdir -p "$state" "$work"
 fi
 lock
 if [ -f "$state/owner" ]; then check; else
  printf '%s\n' "$owner" > "$state/owner"
  printf '%s %s\n' "$claim" "$generation" > "$state/claim"
 fi
 mkdir -p "$state/jobs" "$state/git"
 printf '%s\n' 'set -g prefix None' 'set -g status off' 'set -g escape-time 0' 'set -g history-limit 50000' 'set -g set-clipboard on' 'set -g allow-passthrough on' 'set -g default-terminal tmux-256color' > "$state/tmux.conf"
 git --version; tmux -V; uname -sm
 exit
fi
lock
check
case "$op" in
 check-owner) ;;
 destroy)
  [ "$state" != /wi ] || fail 'Docker destruction belongs to host'
  [ -f "$state/releasing" ] || fail 'workspace must be released before destruction'
  parent=$(dirname "$state")
  [ "$(cd "$state" && pwd -P)" = "$state" ] || fail 'state directory was replaced'
  unlock
  rm -rf -- "$parent"
  ;;
 status)
  cd "$work"
  git symbolic-ref --short HEAD
  git rev-parse HEAD
  git status --porcelain --untracked-files=normal
  ;;
 seed)
  ready
  if [ -f "$state/seeded" ]; then cat >/dev/null; exit; fi
  branch=$1 name=$2 email=$3
  git check-ref-format "refs/heads/$branch" || fail 'invalid branch'
  cat > "$state/git/origin.bundle.tmp"
  mv "$state/git/origin.bundle.tmp" "$state/git/origin.bundle"
  git init -q "$work"
  cd "$work"
  git config core.hooksPath /dev/null
  git config commit.gpgsign false
  git config user.name "$name"
  git config user.email "$email"
  git config remote.origin.url "$state/git/origin.bundle"
  git config remote.origin.fetch '+refs/heads/*:refs/remotes/origin/*'
  git fetch -q origin
  git checkout -q -B "$branch" "refs/remotes/origin/$branch"
  touch "$state/seeded"
  ;;
 update)
  ready
  cat > "$state/git/origin.bundle.tmp"
  cd "$work"
  git bundle verify "$state/git/origin.bundle.tmp"
  mv "$state/git/origin.bundle.tmp" "$state/git/origin.bundle"
  git fetch -q origin
  ;;
 export)
  cd "$work"
  git bundle create "$state/git/export.bundle" "refs/heads/$1"
  cat "$state/git/export.bundle"
  ;;
 exec)
  ready
  cwd "$1"; shift
  timeout -k 2 60 "$@"
  ;;
 job-start)
  ready
  id=$1 runtime=$2 directory=$3 command=$4 seconds=$5
  dir=$state/jobs/$id
  if [ -d "$dir" ]; then exit; fi
  # Keep at most 32 completed jobs; running jobs are never pruned.
  count=0
  ls -td "$state"/jobs/* 2>/dev/null | while IFS= read -r old; do
   [ -f "$old/exit" ] || continue
   count=$((count+1)); [ "$count" -lt 32 ] || rm -rf "$old"
  done
  cwd "$directory"
  mkdir "$dir"
  printf '%s\n' "$runtime" > "$dir/runtime"
  : > "$dir/out"
  [ "$seconds" -gt 0 ] || seconds=86400
  # timeout runs the command in its own process group. The tmux-held wrapper
  # waits outside that group and records completion even after cancellation.
  mux new-session -d -s "job-$id" -c "$directory" sh -c '
   dir=$1 seconds=$2 command=$3
   (ulimit -f 16384; exec timeout -k 2 "$seconds" bash -c "$command") > "$dir/out" 2>&1 &
   pid=$!
   printf "%s\n" "$pid" > "$dir/pid"
   wait "$pid"; rc=$?
   printf "%s\n" "$rc" > "$dir/done.tmp"
   mv "$dir/done.tmp" "$dir/exit"
  ' wi-job "$dir" "$seconds" "$command" || {
   # No job was admitted. Remove the reservation so a retry can start it.
   rm -rf "$dir"
   fail 'tmux could not start job'
  }
  n=0
  until [ -f "$dir/pid" ] || [ -f "$dir/exit" ]; do
   n=$((n+1)); [ "$n" -lt 100 ] || {
    mux kill-session -t "job-$id" 2>/dev/null || true
    printf '125\n' > "$dir/exit"
    fail 'job did not start'
   }
   sleep 0.01
  done
  ;;
 job-status)
  dir=$state/jobs/$1
  if [ ! -f "$dir/exit" ] && ! mux has-session -t "job-$1" 2>/dev/null; then printf '137\n' > "$dir/exit"; fi
  size=$(wc -c < "$dir/out")
  rc=null; [ ! -f "$dir/exit" ] || rc=$(cat "$dir/exit")
  printf '{"size":%s,"exit":%s}\n' "$size" "$rc"
  ;;
 job-read)
  tail -c +$(($2+1)) "$state/jobs/$1/out" | head -c 65536
  ;;
 job-cancel)
  dir=$state/jobs/$1
  # Cancellation can arrive before submission over another connection. Keep a
  # tombstone so the delayed start cannot admit a job after abort succeeded.
  if [ ! -d "$dir" ]; then mkdir "$dir"; : > "$dir/out"; fi
  cancel "$dir"
  ;;
 stop-jobs) stop_jobs ;;
 release)
  touch "$state/releasing"
  stop_jobs
  mux kill-server 2>/dev/null || true
  ;;
 resume) rm -f "$state/releasing" ;;
 rebase)
  ready; cd "$work"
  [ -z "$(git status --porcelain)" ] || fail 'dirty checkout'
  git -c core.hooksPath=/dev/null rebase "$1" || { git rebase --abort; exit 1; }
  ;;
 reset)
  ready; cd "$work"
  [ "$(git rev-parse HEAD)" = "$1" ] || fail 'executor HEAD changed; refusing rollback'
  [ -z "$(git status --porcelain)" ] || fail 'dirty checkout; refusing rollback'
  git reset --hard "$2"
  ;;
 terminal)
  ready
  mux has-session -t shell 2>/dev/null || mux new-session -d -s shell -c "$work" /bin/bash
  unlock
  exec tmux -S "$socket" attach-session -t shell
  ;;
 copy-mode) mux copy-mode -t shell ;;
 *) fail 'unknown operation';;
esac
`
