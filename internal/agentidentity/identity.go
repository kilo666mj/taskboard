package agentidentity

import (
	"hash/fnv"
	"strings"
	"unicode"
)

// words are deliberately short, distinct when spoken, and free of hierarchy.
// Single-word names cover the normal case; two-word combinations provide ample
// collision-free capacity without exposing opaque IDs in the interface.
var words = []string{
	"Amber", "Aspen", "Atlas", "Birch", "Bramble", "Brook", "Cedar", "Cinder",
	"Clover", "Comet", "Coral", "Cove", "Dawn", "Delta", "Dune", "Echo",
	"Elm", "Ember", "Fern", "Finch", "Flint", "Frost", "Grove", "Harbor",
	"Hazel", "Iris", "Jade", "Juniper", "Lark", "Laurel", "Maple", "Meadow",
	"Mica", "Moss", "Nova", "Oak", "Ocean", "Olive", "Onyx", "Orbit",
	"Pebble", "Pine", "Plum", "Quartz", "Rain", "Reed", "River", "Robin",
	"Rowan", "Sage", "Sky", "Slate", "Sol", "Sparrow", "Spruce", "Star",
	"Stone", "Tide", "Vale", "Violet", "Willow", "Wren", "Yarrow", "Zephyr",
}

// Candidates returns friendly callsigns in a stable seed-dependent order. The
// caller chooses the first name not used by another live run.
func Candidates(seed string) []string {
	start := int(hash(seed) % uint64(len(words)))
	result := make([]string, 0, len(words)+len(words)*len(words))
	for offset := range len(words) {
		result = append(result, words[(start+offset)%len(words)])
	}
	for leftOffset := range len(words) {
		left := words[(start+leftOffset)%len(words)]
		for rightOffset := 1; rightOffset < len(words); rightOffset++ {
			right := words[(start+leftOffset+rightOffset)%len(words)]
			result = append(result, left+" "+right)
		}
	}
	return result
}

// Tone is a stable palette slot for an agent-run avatar. Text remains the
// primary identifier, so callers must not rely on color alone.
func Tone(seed string) int { return int(hash(seed) % 8) }

// Normalize validates an operator-provided display name. Callsigns are display
// metadata only; authenticated principal and run IDs remain authoritative.
func Normalize(value string) (string, bool) {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || len([]rune(value)) > 32 {
		return "", false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == ' ' || character == '-' || character == '\'' {
			continue
		}
		return "", false
	}
	return value, true
}

func hash(value string) uint64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(value))
	return hasher.Sum64()
}
