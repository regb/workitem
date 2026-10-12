package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const MaxBundle = 256 << 20

// Git always runs in host-owned directories. No executor config is copied back.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	argv := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "-c", "protocol.file.allow=always", "-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", args[0], strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Bundle makes a self-contained bundle with just the named refs. A temporary
// bare repository supplies ordinary refs/heads names without moving host refs.
func Bundle(ctx context.Context, root, branch, commit, base string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "wi-executor-seed-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	fail := func(err error) (string, func(), error) { cleanup(); return "", func() {}, err }
	if _, err := Git(ctx, root, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fail(err)
	}
	if format, _ := Git(ctx, root, "config", "--get", "extensions.refStorage"); format == "reftable" {
		return fail(fmt.Errorf("executor checkout does not support reftable repositories"))
	}
	if pointers, _ := Git(ctx, root, "grep", "-l", "-F", "version https://git-lfs.github.com/spec/v1", commit, "--"); pointers != "" {
		return fail(fmt.Errorf("executor checkout does not support Git LFS pointers"))
	}
	if modules, _ := Git(ctx, root, "ls-tree", "-r", commit); strings.Contains(modules, "160000 commit ") {
		return fail(fmt.Errorf("executor checkout does not support submodules"))
	}
	if _, err := Git(ctx, dir, "init", "--bare", "--quiet"); err != nil {
		return fail(err)
	}
	if _, err := Git(ctx, dir, "fetch", "--quiet", "--no-tags", root, commit+":refs/heads/"+branch); err != nil {
		return fail(err)
	}
	refs := []string{"refs/heads/" + branch}
	if base != "" && base != branch {
		if _, err := Git(ctx, dir, "fetch", "--quiet", "--no-tags", root, "refs/heads/"+base+":refs/heads/"+base); err != nil {
			return fail(err)
		}
		refs = append(refs, "refs/heads/"+base)
	}
	path := filepath.Join(dir, "origin.bundle")
	if _, err := Git(ctx, dir, append([]string{"bundle", "create", path}, refs...)...); err != nil {
		return fail(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if info.Size() > MaxBundle {
		return fail(fmt.Errorf("executor seed exceeds 256 MiB"))
	}
	return path, cleanup, nil
}

func QuarantineRef(item string, generation uint64) string {
	return fmt.Sprintf("refs/wi/executor/%s/%d/head", item, generation)
}

func (c Client) Import(ctx context.Context, root, item, branch string) (string, error) {
	file, err := os.CreateTemp("", "wi-executor-return-*.bundle")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	writer := &bundleWriter{writer: file}
	exportErr := c.Export(ctx, branch, writer)
	closeErr := file.Close()
	if exportErr != nil {
		return "", exportErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if _, err := Git(ctx, root, "bundle", "verify", file.Name()); err != nil {
		return "", err
	}
	ref := QuarantineRef(item, c.Identity.Generation)
	if _, err := Git(ctx, root, "fetch", "--no-tags", "--no-write-fetch-head", file.Name(), "+refs/heads/"+branch+":"+ref); err != nil {
		return "", err
	}
	return Git(ctx, root, "rev-parse", ref+"^{commit}")
}

type bundleWriter struct {
	writer io.Writer
	size   int
}

func (w *bundleWriter) Write(data []byte) (int, error) {
	if w.size+len(data) > MaxBundle {
		return 0, fmt.Errorf("executor export exceeds 256 MiB")
	}
	n, err := w.writer.Write(data)
	w.size += n
	return n, err
}
