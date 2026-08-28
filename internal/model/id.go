package model

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// NewID returns a sortable, ULID-like 26-character identifier.
func NewID() (string, error) {
	return NewIDWith(time.Now(), rand.Reader)
}

// NewIDWith returns a sortable identifier using the supplied timestamp and entropy.
func NewIDWith(t time.Time, entropy io.Reader) (string, error) {
	if entropy == nil {
		return "", fmt.Errorf("entropy reader is nil")
	}

	var b [16]byte
	ms := uint64(t.UnixNano() / int64(time.Millisecond))
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := io.ReadFull(entropy, b[6:]); err != nil {
		return "", fmt.Errorf("read id entropy: %w", err)
	}
	return encodeBase32Fixed(b[:]), nil
}

func encodeBase32Fixed(b []byte) string {
	var n big.Int
	n.SetBytes(b)
	base := big.NewInt(32)
	zero := big.NewInt(0)
	mod := new(big.Int)
	out := make([]byte, 26)
	for i := len(out) - 1; i >= 0; i-- {
		if n.Cmp(zero) == 0 {
			out[i] = crockford[0]
			continue
		}
		n.DivMod(&n, base, mod)
		out[i] = crockford[mod.Int64()]
	}
	return string(out)
}

// ValidID reports whether s is a wi work-item identifier.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ShortID returns a fixed short display prefix of a work-item ID.
func ShortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

// UniqueIDPrefixes returns the shortest unique display prefix for each ID.
// Prefixes are at least min characters long when the ID is that long.
func UniqueIDPrefixes(ids []string, min int) map[string]string {
	if min < 1 {
		min = 1
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		prefixLen := min
		if prefixLen > len(id) {
			prefixLen = len(id)
		}
		for prefixLen < len(id) && !prefixUnique(id, ids, prefixLen) {
			prefixLen++
		}
		out[id] = id[:prefixLen]
	}
	return out
}

func prefixUnique(id string, ids []string, prefixLen int) bool {
	if prefixLen > len(id) {
		prefixLen = len(id)
	}
	prefix := id[:prefixLen]
	matches := 0
	for _, other := range ids {
		if len(other) < prefixLen {
			continue
		}
		if other[:prefixLen] == prefix {
			matches++
			if matches > 1 {
				return false
			}
		}
	}
	return matches == 1
}
