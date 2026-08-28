package contract

// ResolveOptions identifies an explicit or contextual work item.
type ResolveOptions struct {
	Selector string
	CWD      string
	Env      map[string]string
}
