package git

import (
	"os"
	"strings"
)

// Git operations in this adapter select their repository explicitly. Inherited
// hook/agent repository variables must not redirect them to a different index,
// working tree, namespace, or object store. Keep normal Git config and auth env.
func repositoryEnvironment() []string { return RepositoryEnvironment() }

// RepositoryEnvironment removes inherited repository selectors while preserving config and authentication.
// Use it for Git commands outside this adapter too.
func RepositoryEnvironment() []string {
	result := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_SHALLOW_FILE", "GIT_QUARANTINE_PATH":
			continue
		}
		result = append(result, entry)
	}
	return result
}
