package apitest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/regb/workitem/internal/testutil"
)

// Binaries are a wi executable and the fake pi driver, built from this
// checkout for tests that run real processes. They are private to the test
// process and never installed.
type Binaries struct {
	// Wi is the wi executable: a daemon's runtime hosts run it.
	Wi string
	// Driver is the fake pi driver (internal/apitest/driver).
	Driver string
}

var built struct {
	once     sync.Once
	binaries Binaries
	err      error
}

// Build builds wi and the fake driver once per test process, inside the
// private root testutil.RunIsolatedTests made for it, which removes them when
// the tests end. Its TestMain must use RunIsolatedTests.
func Build(t testing.TB) Binaries {
	t.Helper()
	built.once.Do(func() {
		var paths []string
		paths, built.err = build("./cmd/wi", "./internal/apitest/driver")
		if built.err == nil {
			built.binaries = Binaries{Wi: paths[0], Driver: paths[1]}
		}
	})
	if built.err != nil {
		t.Fatal(built.err)
	}
	return built.binaries
}

var commands struct {
	sync.Mutex
	built map[string]*command
}

type command struct {
	once sync.Once
	path string
	err  error
}

// Command builds the main package pkg, a path from the repository root such
// as "./cmd/wi-github", once per test process, as Build does, and returns its
// executable.
func Command(t testing.TB, pkg string) string {
	t.Helper()
	commands.Lock()
	if commands.built == nil {
		commands.built = map[string]*command{}
	}
	c := commands.built[pkg]
	if c == nil {
		c = &command{}
		commands.built[pkg] = c
	}
	commands.Unlock()
	c.once.Do(func() {
		var paths []string
		if paths, c.err = build(pkg); c.err == nil {
			c.path = paths[0]
		}
	})
	if c.err != nil {
		t.Fatal(c.err)
	}
	return c.path
}

// build builds main packages into a new private directory, in one go build
// so that they share its work, and returns their executables.
func build(pkgs ...string) ([]string, error) {
	root, ok := testutil.IsolatedRoot()
	if !ok {
		return nil, fmt.Errorf("apitest builds need a TestMain that uses testutil.RunIsolatedTests")
	}
	dir, err := os.MkdirTemp(root, "bin-")
	if err != nil {
		return nil, err
	}
	_, file, _, _ := runtime.Caller(0)
	cmd := exec.Command("go", append([]string{"build", "-o", dir + string(filepath.Separator)}, pkgs...)...)
	cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build %v: %v\n%s", pkgs, err, out)
	}
	paths := make([]string, len(pkgs))
	for i, pkg := range pkgs {
		paths[i] = filepath.Join(dir, filepath.Base(pkg))
	}
	return paths, nil
}
