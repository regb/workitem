package model

import "encoding/json"

// RepositorySettings is operator-owned entity state. Namespaces are preserved
// when an extension is unavailable; only registered options can be activated.
type RepositorySettings struct {
	RepositoryID string                                `json:"repository_id"`
	Revision     uint64                                `json:"revision"`
	Namespaces   map[string]map[string]json.RawMessage `json:"namespaces"`
}
