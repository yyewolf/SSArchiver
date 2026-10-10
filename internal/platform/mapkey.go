package platform

import (
	"strconv"
	"strings"
)

// MapKey is the cross-platform grouping key of a map difficulty (spec §4.5):
// lowercase song hash, game mode without ScoreSaber's "Solo" prefix, and the
// 1..9 difficulty. db.Migrate applies the same rule in SQL to legacy rows.
func MapKey(songHash, gameMode string, difficulty int) string {
	return strings.ToLower(songHash) + "/" + strings.TrimPrefix(gameMode, "Solo") + "/" + strconv.Itoa(difficulty)
}
