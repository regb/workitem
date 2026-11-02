package model

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// RepositoryEnvironment is a registered repository's environment variables,
// which Wi sets for the agents, setup commands and commands it starts in
// the repository's checkouts. Values may be secrets.
type RepositoryEnvironment struct {
	RepositoryID string   `json:"repository_id"`
	Revision     uint64   `json:"revision"`
	Vars         []EnvVar `json:"vars"`
	// ChangedAt is when the variables last changed; agents started earlier
	// run with the previous ones.
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

// EnvVar is one environment variable.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidEnvKey accepts a shell variable name.
func ValidEnvKey(key string) error {
	if len(key) > 256 || !envKey.MatchString(key) {
		return fmt.Errorf("%q is not a valid variable name: use letters, digits and _, not starting with a digit", key)
	}
	return nil
}

// ValidEnv checks variables: valid names, each once, values without NUL.
func ValidEnv(vars []EnvVar) error {
	seen := map[string]bool{}
	for _, v := range vars {
		if err := ValidEnvKey(v.Key); err != nil {
			return err
		}
		if strings.HasPrefix(v.Key, "WI_") || v.Key == "TMUX" || v.Key == "TMUX_PANE" {
			return fmt.Errorf("%s is reserved for Wi and tmux", v.Key)
		}
		if seen[v.Key] {
			return fmt.Errorf("%s is set twice", v.Key)
		}
		seen[v.Key] = true
		if strings.ContainsRune(v.Value, 0) || len(v.Value) > 64<<10 {
			return fmt.Errorf("the value of %s must be text of at most 64 KiB", v.Key)
		}
	}
	return nil
}

// MergeEnv updates vars with set, keeping their order and appending new
// keys, then removes unset.
func MergeEnv(vars, set []EnvVar, unset []string) []EnvVar {
	result := slices.Clone(vars)
	for _, v := range set {
		if at := slices.IndexFunc(result, func(e EnvVar) bool { return e.Key == v.Key }); at >= 0 {
			result[at].Value = v.Value
		} else {
			result = append(result, v)
		}
	}
	return slices.DeleteFunc(result, func(e EnvVar) bool { return slices.Contains(unset, e.Key) })
}

// EnvChanges names the keys added, changed or removed from before to after,
// in order; a reordering alone changes nothing.
func EnvChanges(before, after []EnvVar) []string {
	old := map[string]string{}
	for _, v := range before {
		old[v.Key] = v.Value
	}
	changed := []string{}
	for _, v := range after {
		if value, ok := old[v.Key]; !ok || value != v.Value {
			changed = append(changed, v.Key)
		}
		delete(old, v.Key)
	}
	for _, v := range before {
		if _, removed := old[v.Key]; removed {
			changed = append(changed, v.Key)
		}
	}
	return changed
}
