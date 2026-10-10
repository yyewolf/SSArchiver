package platform

import (
	"crypto/rand"
	"regexp"
)

// InternalIDBase is the first internal row ID of leaderboards and scores of
// non-legacy platforms (spec §4.5). Legacy (ScoreSaber) rows keep the
// platform's own numeric IDs, which are far below it.
const InternalIDBase int64 = 1 << 62

// idAlphabet is lowercase base32 without i, l, o and u.
const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// NewPlayerID returns a random opaque player ID (spec §4.2): 12 characters,
// the first one a letter so it never looks like a legacy all-digit ID.
func NewPlayerID() string {
	var b [12]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error (it panics on failure)
	out := make([]byte, len(b))
	out[0] = idAlphabet[10+int(b[0])%22]
	for i := 1; i < len(b); i++ {
		out[i] = idAlphabet[int(b[i])%32]
	}
	return string(out)
}

var playerIDRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// ValidPlayerID reports whether id is a well-formed (path-safe) player ID:
// legacy all-digit IDs and opaque IDs alike.
func ValidPlayerID(id string) bool { return playerIDRe.MatchString(id) }
