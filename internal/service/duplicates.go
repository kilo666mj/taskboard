package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

// duplicateScanLimit bounds how many recently updated open tasks a start or
// create compares against.
const duplicateScanLimit = 2000

// maxDuplicateCandidates bounds how many similar tasks a refusal lists.
const maxDuplicateCandidates = 5

// DuplicateCandidatesError refuses an agent task start or create because
// similar open tasks already exist. It wraps ErrConflict.
type DuplicateCandidatesError struct {
	Candidates []model.DuplicateCandidate
}

func (e *DuplicateCandidatesError) Error() string {
	var builder strings.Builder
	builder.WriteString("similar open tasks already exist: ")
	for index, candidate := range e.Candidates {
		if index > 0 {
			builder.WriteString("; ")
		}
		fmt.Fprintf(&builder, "%s %q (%s", candidate.TaskID, candidate.Title, candidate.Status)
		if candidate.Owner != "" {
			fmt.Fprintf(&builder, ", owner %s", candidate.Owner)
		}
		fmt.Fprintf(&builder, ", version %d)", candidate.Version)
	}
	builder.WriteString(". Claim the matching task with task_claim and continue it, or retry with force_new true only if this is separate work")
	return builder.String()
}

func (e *DuplicateCandidatesError) Unwrap() error { return ErrConflict }

// findDuplicateCandidates returns open tasks visible to the principal whose
// titles closely match, within the same repository and project when both
// sides name one.
func (s *Service) findDuplicateCandidates(ctx context.Context, principal Principal, title, project, repository string) ([]model.DuplicateCandidate, error) {
	wanted := titleTokens(title)
	if len(wanted) == 0 {
		return nil, nil
	}
	heads, err := s.store.ListOpenTaskHeads(ctx, duplicateScanLimit)
	if err != nil {
		return nil, err
	}
	type scored struct {
		candidate model.DuplicateCandidate
		score     float64
	}
	var matches []scored
	for _, head := range heads {
		if !CanView(head, principal) || !sameScope(project, head.Project) || !sameScope(repository, head.Repository) {
			continue
		}
		score, similar := titleSimilarity(wanted, titleTokens(head.Title))
		if !similar {
			continue
		}
		matches = append(matches, scored{score: score, candidate: model.DuplicateCandidate{
			TaskID: head.ID, Title: head.Title, Status: head.Status, Owner: head.Owner, Repository: head.Repository, Version: head.Version,
		}})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	if len(matches) > maxDuplicateCandidates {
		matches = matches[:maxDuplicateCandidates]
	}
	candidates := make([]model.DuplicateCandidate, 0, len(matches))
	for _, match := range matches {
		candidates = append(candidates, match.candidate)
	}
	return candidates, nil
}

// checkDuplicates refuses new agent work that resembles open tasks unless the
// caller forces it, and returns the IDs a forced request went past.
func (s *Service) checkDuplicates(ctx context.Context, principal Principal, force bool, title, project, repository string) ([]string, error) {
	candidates, err := s.findDuplicateCandidates(ctx, principal, title, project, repository)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	if !force {
		return nil, &DuplicateCandidatesError{Candidates: candidates}
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.TaskID)
	}
	return ids, nil
}

// sameScope treats an unset project or repository as compatible with any, and
// compares repositories by name so "owner/name" matches "name".
func sameScope(left, right string) bool {
	left, right = scopeName(left), scopeName(right)
	return left == "" || right == "" || left == right
}

func scopeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimSuffix(strings.TrimSuffix(value, "/"), ".git")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		value = value[index+1:]
	}
	return value
}

var titleStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true, "for": true,
	"from": true, "in": true, "into": true, "is": true, "it": true, "of": true, "on": true, "or": true, "the": true,
	"to": true, "via": true, "with": true,
}

func titleTokens(title string) map[string]bool {
	tokens := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if titleStopWords[word] || len(word) < 2 && !unicode.IsDigit(rune(word[0])) {
			continue
		}
		tokens[word] = true
	}
	return tokens
}

// titleSimilarity reports whether two token sets describe the same work.
// Titles match when most of their words are shared, or when one title is
// largely contained in the other and they still share a fair share overall.
func titleSimilarity(left, right map[string]bool) (float64, bool) {
	if len(left) == 0 || len(right) == 0 {
		return 0, false
	}
	shared := 0
	for token := range left {
		if right[token] {
			shared++
		}
	}
	union := len(left) + len(right) - shared
	smaller := min(len(left), len(right))
	jaccard := float64(shared) / float64(union)
	containment := float64(shared) / float64(smaller)
	if smaller < 2 {
		return jaccard, jaccard == 1
	}
	return jaccard, jaccard >= 0.65 || shared >= 3 && containment >= 0.8 && jaccard >= 0.3
}

func overriddenDuplicatesPayload(ids []string) map[string]any {
	if len(ids) == 0 {
		return nil
	}
	return map[string]any{"overridden_duplicates": ids}
}

// resolveDuplicateRoot follows duplicate links from the target to the task
// that was kept, refusing links back to the task being marked.
func resolveDuplicateRoot(ctx context.Context, tx *store.Tx, targetID, taskID string) (string, string, error) {
	id := targetID
	for range 20 {
		if id == taskID {
			return "", "", fmt.Errorf("%w: a task cannot be a duplicate of itself", ErrValidation)
		}
		var next sql.NullString
		var title string
		err := tx.QueryRowContext(ctx, `SELECT duplicate_of,title FROM tasks WHERE id=?`, id).Scan(&next, &title)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("%w: duplicate_of task not found", ErrValidation)
		}
		if err != nil {
			return "", "", err
		}
		if next.String == "" {
			return id, title, nil
		}
		id = next.String
	}
	return "", "", fmt.Errorf("%w: duplicate links are too deep", ErrValidation)
}

func duplicateClaimError(keptID string) error {
	return fmt.Errorf("%w: task is a duplicate of %s; claim that task instead", ErrValidation, keptID)
}
