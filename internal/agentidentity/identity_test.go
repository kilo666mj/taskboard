package agentidentity

import "testing"

func TestCandidatesAreStableFriendlyAndDistinct(t *testing.T) {
	first, second := Candidates("run-1"), Candidates("run-1")
	if len(first) < 1000 || first[0] != second[0] {
		t.Fatalf("candidates are not stable: %d, %q / %q", len(first), first[0], second[0])
	}
	seen := map[string]bool{}
	for _, candidate := range first {
		if _, ok := Normalize(candidate); !ok {
			t.Fatalf("invalid generated callsign %q", candidate)
		}
		if seen[candidate] {
			t.Fatalf("duplicate generated callsign %q", candidate)
		}
		seen[candidate] = true
	}
}

func TestNormalizeCallsign(t *testing.T) {
	if got, ok := Normalize("  Maple   Finch "); !ok || got != "Maple Finch" {
		t.Fatalf("Normalize = %q, %v", got, ok)
	}
	for _, invalid := range []string{"", "Maple/Finch", "Maple 🍁", "A callsign that is deliberately much too long"} {
		if _, ok := Normalize(invalid); ok {
			t.Fatalf("Normalize(%q) unexpectedly succeeded", invalid)
		}
	}
}
