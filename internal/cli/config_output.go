package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

type configKeyNotFoundError struct{ key string }

func (e configKeyNotFoundError) Error() string {
	return fmt.Sprintf("unknown configuration key %q", e.key)
}
func printConfigTree(w io.Writer, prefix string, value any) error {
	if object, ok := value.(map[string]any); ok && len(object) > 0 {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if err := printConfigTree(w, path, object[key]); err != nil {
				return err
			}
		}
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s = %s\n", prefix, encoded)
	return err
}
