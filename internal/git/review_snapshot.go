package git

import (
	"bytes"
	"context"
	"crypto/sha1" // Git SHA-1 object identities, not an authentication primitive.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/regb/workitem/internal/workspacefiles"
)

const SnapshotFileLimit = 8 << 20
const SnapshotTotalLimit = 64 << 20
const SnapshotPathLimit = 20000

var ErrSnapshotChanged = errors.New("workspace changed during checkpoint capture; retry with a new capture identity")

type SnapshotExclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type ReviewSnapshot struct {
	Commit   string `json:"commit"`
	Tree     string `json:"tree"`
	Head     string `json:"head"`
	Detached bool
	Branch   string              `json:"branch"`
	Excluded []SnapshotExclusion `json:"excluded,omitempty"`
}
type snapshotEntry struct {
	mode, oid string
	data      []byte
	exists    bool
	reason    string
}

func (c Client) snapshotCommand(ctx context.Context, dir, index string, input io.Reader, args ...string) ([]byte, error) {
	return c.snapshotCommandAs(ctx, dir, index, input, [2]string{"Wi checkpoint", "wi@localhost"}, args...)
}
func (c Client) snapshotCommandAs(ctx context.Context, dir, index string, input io.Reader, identity [2]string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	all := []string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.splitIndex=false", "-c", "index.sparse=false", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-C", dir}
	cmd := exec.CommandContext(ctx, c.Path, append(all, args...)...)
	date := time.Now().UTC().Format(time.RFC3339)
	cmd.Env = append(repositoryEnvironment(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date, "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME="+identity[0], "GIT_AUTHOR_EMAIL="+identity[1], "GIT_COMMITTER_NAME="+identity[0], "GIT_COMMITTER_EMAIL="+identity[1])
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.Stdin = input
	cmd.WaitDelay = time.Second
	out, stderr := limitedReviewBuffer{limit: reviewOutputLimit}, limitedReviewBuffer{limit: 64 << 10}
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git checkpoint: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
func objectID(kind string, data []byte, sha256Repo bool) string {
	header := []byte(fmt.Sprintf("%s %d\x00", kind, len(data)))
	if sha256Repo {
		h := sha256.New()
		h.Write(header)
		h.Write(data)
		return hex.EncodeToString(h.Sum(nil))
	}
	h := sha1.New()
	h.Write(header)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func (c Client) snapshotPaths(ctx context.Context, dir, head string) (map[string]snapshotEntry, map[string]bool, []string, error) {
	data, err := c.snapshotCommand(ctx, dir, "", nil, "ls-tree", "-r", "-z", head)
	if err != nil {
		return nil, nil, nil, err
	}
	base := map[string]snapshotEntry{}
	paths := map[string]bool{}
	for _, raw := range bytes.Split(data, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		parts := bytes.SplitN(raw, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, nil, nil, errors.New("malformed snapshot tree")
		}
		fields := strings.Fields(string(parts[0]))
		if len(fields) != 3 {
			return nil, nil, nil, errors.New("malformed snapshot entry")
		}
		p := string(parts[1])
		base[p] = snapshotEntry{mode: fields[0], oid: fields[2], exists: true}
		paths[p] = true
	}
	data, err = c.snapshotCommand(ctx, dir, "", nil, "ls-files", "-t", "-z")
	if err != nil {
		return nil, nil, nil, err
	}
	sparse := map[string]bool{}
	for _, raw := range bytes.Split(data, []byte{0}) {
		if len(raw) < 3 {
			continue
		}
		p := string(raw[2:])
		paths[p] = true
		sparse[p] = raw[0] == 'S'
	}
	data, err = c.snapshotCommand(ctx, dir, "", nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, raw := range bytes.Split(data, []byte{0}) {
		if len(raw) > 0 {
			paths[string(raw)] = true
		}
	}
	if len(paths) > SnapshotPathLimit {
		return nil, nil, nil, errors.New("checkpoint path limit exceeded")
	}
	ordered := make([]string, 0, len(paths))
	for p := range paths {
		ordered = append(ordered, p)
	}
	sort.Strings(ordered)
	return base, sparse, ordered, nil
}
func (c Client) snapshotScan(ctx context.Context, dir, head string, write bool, index string) (string, []SnapshotExclusion, error) {
	base, sparse, paths, err := c.snapshotPaths(ctx, dir, head)
	if err != nil {
		return "", nil, err
	}
	fingerprint := sha256.New()
	var excluded []SnapshotExclusion
	total := 0
	var updates bytes.Buffer
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		old := base[p]
		data, mode, exists, readErr := workspacefiles.SnapshotFile(dir, p, SnapshotFileLimit)
		entry := snapshotEntry{mode: mode, exists: exists, data: data}
		if readErr != nil {
			entry.reason = "unsupported or oversized path"
			entry.exists = old.exists
			entry.mode = old.mode
			entry.oid = old.oid
		}
		if readErr == nil && sparse[p] && !exists {
			entry = old
		}
		if readErr == nil && exists {
			entry.oid = objectID("blob", data, len(head) == 64)
		}
		changed := entry.exists != old.exists || entry.oid != old.oid || entry.mode != old.mode
		// Secret rules apply to changed tracked bytes too. Hashing is local and
		// does not write excluded bytes into the object database.
		if changed && entry.exists && total+len(data) > SnapshotTotalLimit {
			entry.reason = "checkpoint byte limit exceeded"
		}
		fmt.Fprintf(fingerprint, "%d:%s:%s:%s:%t:%s\x00", len(p), p, entry.mode, entry.oid, entry.exists, entry.reason)
		if entry.reason != "" {
			excluded = append(excluded, SnapshotExclusion{p, entry.reason})
			continue
		}
		if !changed {
			continue
		}
		total += len(data)
		if !write {
			continue
		}
		if !entry.exists {
			fmt.Fprintf(&updates, "0 %s\t%s\x00", strings.Repeat("0", len(head)), p)
			continue
		}
		oid, err := c.snapshotCommand(ctx, dir, index, bytes.NewReader(data), "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(string(oid)) != entry.oid {
			return "", nil, errors.New("Git object format mismatch")
		}
		fmt.Fprintf(&updates, "%s %s\t%s\x00", entry.mode, entry.oid, p)
	}
	if write && updates.Len() > 0 {
		if _, err := c.snapshotCommand(ctx, dir, index, &updates, "update-index", "-z", "--index-info"); err != nil {
			return "", nil, err
		}
	}
	return hex.EncodeToString(fingerprint.Sum(nil)), excluded, nil
}

// CaptureReviewSnapshot writes only unreachable objects. Publication belongs to
// the coordinator, after it revalidates claim/runtime ownership. No real index,
// user branch, filters, hooks, or stash are changed.
func (c Client) CaptureReviewSnapshot(ctx context.Context, dir, expectedHead, expectedBranch string) (ReviewSnapshot, error) {
	if err := c.EnsureNoOperationInProgress(ctx, dir, "capture checkpoint"); err != nil {
		return ReviewSnapshot{}, err
	}
	head, err := c.ReviewResolve(ctx, dir, "HEAD")
	if err != nil {
		return ReviewSnapshot{}, fmt.Errorf("checkpoint requires an existing HEAD: %w", err)
	}
	branch, err := c.CurrentBranch(ctx, dir)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	if expectedHead != "" && head != expectedHead || expectedBranch != "" && branch != expectedBranch {
		return ReviewSnapshot{}, ErrSnapshotChanged
	}
	tmp, err := os.MkdirTemp("", "wi-snapshot-")
	if err != nil {
		return ReviewSnapshot{}, err
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	if _, err := c.snapshotCommand(ctx, dir, index, nil, "read-tree", head); err != nil {
		return ReviewSnapshot{}, err
	}
	before, excluded, err := c.snapshotScan(ctx, dir, head, true, index)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	tree, err := c.snapshotCommand(ctx, dir, index, nil, "write-tree")
	if err != nil {
		return ReviewSnapshot{}, err
	}
	after, _, err := c.snapshotScan(ctx, dir, head, false, index)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	actual, err := c.ReviewResolve(ctx, dir, "HEAD")
	if err != nil {
		return ReviewSnapshot{}, err
	}
	actualBranch, err := c.CurrentBranch(ctx, dir)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	if before != after || head != actual || branch != actualBranch {
		return ReviewSnapshot{}, ErrSnapshotChanged
	}
	commit, err := c.snapshotCommand(ctx, dir, index, strings.NewReader("Wi private checkpoint\n"), "commit-tree", strings.TrimSpace(string(tree)), "-p", head)
	if err != nil {
		return ReviewSnapshot{}, err
	}
	return ReviewSnapshot{Commit: strings.TrimSpace(string(commit)), Tree: strings.TrimSpace(string(tree)), Head: head, Branch: branch, Detached: branch == "", Excluded: excluded}, nil
}

// ReviewPatch reads immutable code endpoints without invoking repository tools.
func (c Client) ReviewPatch(ctx context.Context, dir, base, head, p string) ([]byte, error) {
	if !ValidCommitSHA(base) || !ValidCommitSHA(head) {
		return nil, errors.New("full comparison commit IDs are required")
	}
	args := []string{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-renames", base, head, "--"}
	if p != "" {
		if err := workspacefiles.ValidPath(p); err != nil {
			return nil, err
		}
		args = append(args, p)
	}
	return c.reviewOutput(ctx, dir, args...)
}

func (c Client) DeleteItemReviewRefs(ctx context.Context, dir, item string) error {
	if !validReviewItem(item) {
		return errors.New("invalid review item ID")
	}
	prefix := "refs/wi/review/" + item + "/snapshots/"
	refs, err := c.snapshotCommand(ctx, dir, "", nil, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		return err
	}
	var transaction strings.Builder
	transaction.WriteString("start\n")
	for _, line := range strings.Split(strings.TrimSpace(string(refs)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], prefix) || !ValidCommitSHA(parts[1]) {
			return errors.New("invalid retained review ref")
		}
		fmt.Fprintf(&transaction, "delete %s %s\n", parts[0], parts[1])
	}
	transaction.WriteString("prepare\ncommit\n")
	_, err = c.snapshotCommand(ctx, dir, "", strings.NewReader(transaction.String()), "update-ref", "--stdin")
	return err
}

func validReviewItem(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, ch := range s {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", ch) {
			return false
		}
	}
	return true
}

func (c Client) PublishReviewSnapshot(ctx context.Context, dir, ref, commit string) error {
	parts := strings.Split(ref, "/")
	if len(parts) != 6 || parts[0] != "refs" || parts[1] != "wi" || parts[2] != "review" || !validReviewItem(parts[3]) || parts[4] != "snapshots" || parts[5] == "" || !ValidCommitSHA(commit) {
		return errors.New("invalid snapshot ref")
	}
	if _, err := c.snapshotCommand(ctx, dir, "", nil, "check-ref-format", ref); err != nil {
		return err
	}
	_, err := c.snapshotCommand(ctx, dir, "", nil, "update-ref", ref, commit, strings.Repeat("0", len(commit)))
	return err
}
func (c Client) ReviewTree(ctx context.Context, dir, commit string) (string, error) {
	if !ValidCommitSHA(commit) {
		return "", errors.New("invalid commit")
	}
	out, err := c.snapshotCommand(ctx, dir, "", nil, "rev-parse", commit+"^{tree}")
	return strings.TrimSpace(string(out)), err
}
