// Package apperr gives errors a kind that decides how the API reports them,
// such as a stale request (ABORTED), so the code never depends on an error's
// wording. Errors keep their kind across the coordinator protocol: the
// coordinator sends it with the message, and the client's error answers
// errors.Is for it.
package apperr

import (
	"errors"
	"fmt"
)

// Kinds an error can have. Each is a sentinel for errors.Is.
var (
	// Stale is a request made from state that changed since it was read:
	// an expected revision or generation no longer current. Read again,
	// then retry.
	Stale = errors.New("stale request")
	// NotFound is something named that does not exist.
	NotFound = errors.New("not found")
	// Invalid is a malformed or inconsistent request; retrying it as is
	// fails again.
	Invalid = errors.New("invalid request")
	// Exists is something created that already exists.
	Exists = errors.New("already exists")
	// Exhausted is a bounded resource used up, such as a revision counter.
	Exhausted = errors.New("resource exhausted")
)

var kinds = map[string]error{"stale": Stale, "not_found": NotFound, "invalid": Invalid, "exists": Exists, "exhausted": Exhausted}

// Error is an error of a kind. Its message is the whole text; the kind is
// only matched.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Is matches the error's kind.
func (e *Error) Is(target error) bool { return target == e.Kind }

func newf(kind error, format string, args ...any) error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Stalef is a stale request.
func Stalef(format string, args ...any) error { return newf(Stale, format, args...) }

// NotFoundf is something that does not exist.
func NotFoundf(format string, args ...any) error { return newf(NotFound, format, args...) }

// Invalidf is a malformed or inconsistent request.
func Invalidf(format string, args ...any) error { return newf(Invalid, format, args...) }

// Existsf is something that already exists.
func Existsf(format string, args ...any) error { return newf(Exists, format, args...) }

// Exhaustedf is a bounded resource used up.
func Exhaustedf(format string, args ...any) error { return newf(Exhausted, format, args...) }

// KindOf names err's kind for the coordinator protocol, or "" for none.
func KindOf(err error) string {
	for name, kind := range kinds {
		if errors.Is(err, kind) {
			return name
		}
	}
	return ""
}

// ByName is the kind KindOf named, or nil.
func ByName(name string) error { return kinds[name] }
