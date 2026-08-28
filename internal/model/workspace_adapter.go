package model

// WorktreeAddOptions is the adapter-neutral specification for materializing a
// Git worktree.
type WorktreeAddOptions struct {
	RepoRoot   string
	Path       string
	Branch     string
	StartPoint string
	NewBranch  bool
}

// DirenvStatus is the adapter-neutral trust observation for one .envrc.
type DirenvStatus struct {
	Found   bool
	RCPath  string
	Allowed bool
	Raw     string
}
