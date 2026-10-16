package contract

// CheckoutPreparation is a synchronous boundary before claim publication. The
// workspace reservation remains held by the caller throughout preparation.
type CheckoutPreparation struct {
	ItemID       string
	RepositoryID string
	Path         string
	Generation   uint64
	Reused       bool
}
