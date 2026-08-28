package app_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const internalImportPrefix = "github.com/regb/workitem/internal/"

var childPackageImports = map[string]map[string]bool{
	"contract/": {},
	"core/item/": {
		internalImportPrefix + "app/contract": true,
		internalImportPrefix + "model":        true,
	},
	"core/workspace/": {
		internalImportPrefix + "app/contract": true,
		internalImportPrefix + "model":        true,
	},
	"core/primaryagent/": {
		internalImportPrefix + "agent":        true,
		internalImportPrefix + "app/contract": true,
		internalImportPrefix + "model":        true,
		internalImportPrefix + "lock":         true,
		internalImportPrefix + "runtimepath":  true,
	},
	"core/attention/": {
		internalImportPrefix + "app/contract": true,
		internalImportPrefix + "model":        true,
	},
	"view/": {
		internalImportPrefix + "model":    true,
		internalImportPrefix + "priority": true,
	},
	"adapter/tmux/terminal/": {
		internalImportPrefix + "app/contract": true,
		internalImportPrefix + "model":        true,
		internalImportPrefix + "tmux":         true,
	},
	"adapter/tmux/porcelain/": {
		internalImportPrefix + "app/contract": true,
		internalImportPrefix + "model":        true,
	},
}

func TestChildPackageDependencyGraph(t *testing.T) {
	root := "."
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == "package_boundaries_test.go" || filepath.Dir(path) == root || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(path)
		allowed, declared := allowedImportsForPath(rel)
		if !strings.HasSuffix(path, "_test.go") && !declared {
			t.Errorf("%s belongs to an undeclared architectural child package", path)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if importPath == internalImportPrefix+"app" {
				t.Errorf("%s imports parent internal/app", path)
			}
			if strings.HasSuffix(path, "_test.go") || !strings.HasPrefix(importPath, internalImportPrefix) {
				continue
			}
			if declared && !allowed[importPath] {
				t.Errorf("%s imports disallowed dependency %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func allowedImportsForPath(path string) (map[string]bool, bool) {
	for prefix, allowed := range childPackageImports {
		if strings.HasPrefix(path, prefix) {
			return allowed, true
		}
	}
	return nil, false
}
