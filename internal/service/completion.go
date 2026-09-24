package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func (s *Service) ListCompletionRequirementsFor(ctx context.Context, taskID string, principal Principal) ([]model.CompletionRequirement, error) {
	task, err := s.GetFor(ctx, strings.TrimSpace(taskID), principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent {
		canSubmit := principal.HasCapability(CapabilityTaskEvidence) && canMutate(task, principal)
		canValidate := principal.HasCapability(CapabilityTaskValidate) && CanView(task, principal)
		if !canSubmit && !canValidate {
			return nil, ErrForbidden
		}
	}
	return s.store.ListCompletionRequirements(ctx, task.ID)
}

func (s *Service) CreateCompletionRequirementFor(ctx context.Context, taskID string, request model.CreateCompletionRequirementRequest, principal Principal) (model.CompletionRequirement, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.CompletionRequirement{}, ErrForbidden
	}
	taskID = strings.TrimSpace(taskID)
	request.Label = strings.TrimSpace(request.Label)
	if !isRequirementKind(request.Kind) || request.Label == "" || len(request.Label) > 300 || request.ExpectedVersion < 1 {
		return model.CompletionRequirement{}, fmt.Errorf("%w: kind, 1-300 character label, and expected_version are required", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.CompletionRequirement{}, err
	}
	if !canMutate(task, principal) {
		return model.CompletionRequirement{}, ErrForbidden
	}
	now := time.Now().UTC()
	item := model.CompletionRequirement{ID: newID(now), TaskID: taskID, Kind: request.Kind, Label: request.Label, Required: request.Required, Status: model.RequirementPending, CreatedBy: principal.ID, CreatedAt: now, UpdatedAt: now}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return item, err
	}
	if current.Version != request.ExpectedVersion {
		return item, ErrConflict
	}
	if current.Status == model.TaskDone || current.Status == model.TaskCancelled {
		return item, fmt.Errorf("%w: terminal task contracts cannot change", ErrValidation)
	}
	if err = store.InsertCompletionRequirement(ctx, tx, item); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET last_edited_by=?,version=version+1,updated_at=? WHERE id=? AND version=?`, principal.ID, stamp(now), taskID, request.ExpectedVersion); err != nil {
		return item, err
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.completion_requirement_added", Actor: principal.ID, Message: "Completion requirement added", Payload: map[string]any{"requirement_id": item.ID, "kind": item.Kind, "required": item.Required, "version": request.ExpectedVersion + 1}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return item, err
	}
	if err = tx.Commit(); err == nil {
		s.publish(event)
	}
	return item, err
}

func (s *Service) SubmitCompletionEvidenceFor(ctx context.Context, taskID, requirementID string, request model.SubmitCompletionEvidenceRequest, principal Principal) (model.CompletionEvidence, error) {
	taskID, requirementID, request.RunID, request.ReferenceID, request.Note = strings.TrimSpace(taskID), strings.TrimSpace(requirementID), strings.TrimSpace(request.RunID), strings.TrimSpace(request.ReferenceID), strings.TrimSpace(request.Note)
	if request.ReferenceID == "" && request.Note == "" {
		return model.CompletionEvidence{}, fmt.Errorf("%w: reference_id or concise evidence note is required", ErrValidation)
	}
	if len(request.Note) > 2000 {
		return model.CompletionEvidence{}, fmt.Errorf("%w: evidence note is limited to 2000 characters", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.CompletionEvidence{}, err
	}
	if !canMutate(task, principal) {
		return model.CompletionEvidence{}, ErrForbidden
	}
	if principal.Agent {
		if !principal.HasCapability(CapabilityTaskEvidence) {
			return model.CompletionEvidence{}, ErrForbidden
		}
		if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
			return model.CompletionEvidence{}, fmt.Errorf("%w: active owned run is required", ErrValidation)
		}
	} else if !principal.Can(PermissionTaskWrite) {
		return model.CompletionEvidence{}, ErrForbidden
	}
	requirement, err := s.store.GetCompletionRequirement(ctx, requirementID)
	if err != nil || requirement.TaskID != taskID {
		if err == nil {
			err = store.ErrNotFound
		}
		return model.CompletionEvidence{}, err
	}
	if request.ReferenceID != "" {
		reference, refErr := s.store.GetTaskReference(ctx, request.ReferenceID)
		if refErr != nil || reference.TaskID != taskID {
			return model.CompletionEvidence{}, fmt.Errorf("%w: evidence reference must belong to the task", ErrValidation)
		}
		if expected := requiredReferenceKind(requirement.Kind); expected != "" && reference.Kind != expected {
			return model.CompletionEvidence{}, fmt.Errorf("%w: %s evidence requires a %s reference", ErrValidation, requirement.Kind, expected)
		}
	}
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID, RequirementID string
		model.SubmitCompletionEvidenceRequest
	}{taskID, requirementID, request})
	if err != nil {
		return model.CompletionEvidence{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_completion_evidence_submit", request.IdempotencyKey, hash)
	if err != nil {
		return model.CompletionEvidence{}, err
	}
	if replay {
		requirements, _ := s.store.ListCompletionRequirements(ctx, taskID)
		for _, candidate := range requirements {
			for _, evidence := range candidate.Evidence {
				if evidence.ID == fmt.Sprint(event.Payload["evidence_id"]) {
					return evidence, nil
				}
			}
		}
	}
	now := time.Now().UTC()
	item := model.CompletionEvidence{ID: newID(now), RequirementID: requirementID, TaskID: taskID, RunID: request.RunID, ReferenceID: request.ReferenceID, Note: request.Note, Status: model.EvidenceSubmitted, SubmittedBy: principal.ID, SubmittedAt: now}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = store.InsertCompletionEvidence(ctx, tx, item); err != nil {
		return item, err
	}
	payload := map[string]any{"evidence_id": item.ID, "requirement_id": requirementID}
	mergePayload(payload, idempotencyPayload("task_completion_evidence_submit", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.completion_evidence_submitted", Actor: principal.ID, Message: "Completion evidence submitted", Payload: payload, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, created); err != nil {
		return item, err
	}
	if err = tx.Commit(); err == nil {
		s.publish(created)
	}
	return item, err
}

func (s *Service) ReviewCompletionRequirementFor(ctx context.Context, taskID, requirementID string, request model.ReviewCompletionRequest, principal Principal) (model.CompletionRequirement, error) {
	if principal.Agent {
		if !principal.HasCapability(CapabilityTaskValidate) {
			return model.CompletionRequirement{}, ErrForbidden
		}
	} else if !principal.Can(PermissionTaskWrite) {
		return model.CompletionRequirement{}, ErrForbidden
	}
	taskID, requirementID, request.EvidenceID, request.Note, request.SeparationOverrideReason = strings.TrimSpace(taskID), strings.TrimSpace(requirementID), strings.TrimSpace(request.EvidenceID), strings.TrimSpace(request.Note), strings.TrimSpace(request.SeparationOverrideReason)
	if request.ExpectedVersion < 1 {
		return model.CompletionRequirement{}, fmt.Errorf("%w: expected_version is required", ErrValidation)
	}
	if request.Status != model.RequirementSatisfied && request.Status != model.RequirementWaived && request.Status != model.RequirementPending {
		return model.CompletionRequirement{}, fmt.Errorf("%w: status must be satisfied, waived, or pending", ErrValidation)
	}
	if request.Status == model.RequirementWaived && request.Note == "" {
		return model.CompletionRequirement{}, fmt.Errorf("%w: waiver requires a reason", ErrValidation)
	}
	if request.SeparationOverride && request.Status != model.RequirementSatisfied {
		return model.CompletionRequirement{}, fmt.Errorf("%w: separation override is only valid when satisfying a requirement", ErrValidation)
	}
	if request.SeparationOverride && (request.SeparationOverrideReason == "" || len(request.SeparationOverrideReason) > 1000) {
		return model.CompletionRequirement{}, fmt.Errorf("%w: separation override requires a 1-1000 character reason", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.CompletionRequirement{}, err
	}
	if (principal.Agent && !CanView(task, principal)) || (!principal.Agent && !canMutate(task, principal)) {
		return model.CompletionRequirement{}, ErrForbidden
	}
	requirement, err := s.store.GetCompletionRequirement(ctx, requirementID)
	if err != nil || requirement.TaskID != taskID {
		return model.CompletionRequirement{}, store.ErrNotFound
	}
	if request.Status == model.RequirementSatisfied && requirement.Kind != model.RequirementHumanApproval && request.EvidenceID == "" {
		return model.CompletionRequirement{}, fmt.Errorf("%w: satisfying this requirement requires evidence_id", ErrValidation)
	}
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID, RequirementID string
		model.ReviewCompletionRequest
	}{taskID, requirementID, request})
	if err != nil {
		return model.CompletionRequirement{}, err
	}
	request.IdempotencyHash = hash
	replayEvent, replay, err := s.replayIdempotency(ctx, principal, "task_completion_review", request.IdempotencyKey, hash)
	if err != nil {
		return model.CompletionRequirement{}, err
	}
	if replay {
		if replayEvent.Payload["reason_code"] != nil {
			return requirement, fmt.Errorf("%w: %s", ErrValidation, fmt.Sprint(replayEvent.Payload["reason"]))
		}
		return s.store.GetCompletionRequirement(ctx, requirementID)
	}
	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return requirement, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return requirement, err
	}
	if current.Version != request.ExpectedVersion {
		return requirement, ErrConflict
	}
	builders := []string(nil)
	if request.Status == model.RequirementSatisfied {
		builders, err = completionBuilderPrincipals(ctx, tx, taskID, requirementID, request.EvidenceID)
		if err != nil {
			return requirement, err
		}
		selfValidation := slicesContain(builders, principal.ID)
		if request.SeparationOverride && !selfValidation {
			return requirement, fmt.Errorf("%w: separation override requires a builder-validator conflict", ErrValidation)
		}
		if selfValidation && request.SeparationOverride && (principal.Agent || principal.Role != RoleOwner && principal.Role != RoleAdmin) {
			return requirement, s.rejectCompletionReview(ctx, tx, taskID, requirementID, request.EvidenceID, principal.ID, builders, "override_not_authorized", "only owners and admins may override builder-validator separation", request.IdempotencyKey, request.IdempotencyHash, now)
		}
		if selfValidation && !request.SeparationOverride {
			return requirement, s.rejectCompletionReview(ctx, tx, taskID, requirementID, request.EvidenceID, principal.ID, builders, "builder_validator_same_principal", "validator principal is recorded in the task's builder provenance", request.IdempotencyKey, request.IdempotencyHash, now)
		}
	}
	if request.EvidenceID != "" {
		evidenceStatus := model.EvidenceVerified
		if request.Status == model.RequirementPending {
			evidenceStatus = model.EvidenceRejected
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE completion_evidence SET status=?,reviewed_by=?,review_note=?,reviewed_at=? WHERE id=? AND requirement_id=?`, evidenceStatus, principal.ID, request.Note, stamp(now), request.EvidenceID, requirementID)
		if updateErr != nil {
			return requirement, updateErr
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return requirement, fmt.Errorf("%w: evidence not found", ErrValidation)
		}
	}
	verifiedBy, waiver, verifiedAt := "", "", any(nil)
	if request.Status == model.RequirementSatisfied {
		verifiedBy = principal.ID
		verifiedAt = stamp(now)
	}
	if request.Status == model.RequirementWaived {
		verifiedBy = principal.ID
		waiver = request.Note
		verifiedAt = stamp(now)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE completion_requirements SET status=?,verified_by=?,waiver_reason=?,updated_at=?,verified_at=? WHERE id=?`, request.Status, verifiedBy, waiver, stamp(now), verifiedAt, requirementID); err != nil {
		return requirement, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET last_edited_by=?,version=version+1,updated_at=? WHERE id=? AND version=?`, principal.ID, stamp(now), taskID, request.ExpectedVersion)
	if err != nil {
		return requirement, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return requirement, ErrConflict
	}
	payload := map[string]any{"requirement_id": requirementID, "status": request.Status, "evidence_id": request.EvidenceID, "version": request.ExpectedVersion + 1}
	mergePayload(payload, idempotencyPayload("task_completion_review", request.IdempotencyKey, request.IdempotencyHash))
	if request.SeparationOverride {
		payload["separation_override"] = true
		payload["separation_override_reason"] = request.SeparationOverrideReason
		payload["builder_principals"] = builders
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.completion_requirement_reviewed", Actor: principal.ID, Message: "Completion requirement reviewed", Payload: payload, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return requirement, err
	}
	if err = tx.Commit(); err != nil {
		return requirement, err
	}
	s.publish(event)
	return s.store.GetCompletionRequirement(ctx, requirementID)
}

func completionBuilderPrincipals(ctx context.Context, tx *store.Tx, taskID, requirementID, evidenceID string) ([]string, error) {
	builders := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT agent FROM agent_runs WHERE task_id=?`, taskID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var principal string
		if err = rows.Scan(&principal); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if principal = strings.TrimSpace(principal); principal != "" {
			builders[principal] = true
		}
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if evidenceID != "" {
		var submittedBy, referenceID string
		err = tx.QueryRowContext(ctx, `SELECT submitted_by,COALESCE(reference_id,'') FROM completion_evidence WHERE id=? AND requirement_id=?`, evidenceID, requirementID).Scan(&submittedBy, &referenceID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: evidence not found", ErrValidation)
		}
		if err != nil {
			return nil, err
		}
		if submittedBy = strings.TrimSpace(submittedBy); submittedBy != "" {
			builders[submittedBy] = true
		}
		if referenceID != "" {
			var createdBy string
			err = tx.QueryRowContext(ctx, `SELECT created_by FROM task_references WHERE id=? AND task_id=?`, referenceID, taskID).Scan(&createdBy)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: evidence reference not found", ErrValidation)
			}
			if err != nil {
				return nil, err
			}
			if createdBy = strings.TrimSpace(createdBy); createdBy != "" {
				builders[createdBy] = true
			}
		}
	}
	result := make([]string, 0, len(builders))
	for principal := range builders {
		result = append(result, principal)
	}
	sort.Strings(result)
	return result, nil
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) rejectCompletionReview(ctx context.Context, tx *store.Tx, taskID, requirementID, evidenceID, validator string, builders []string, reasonCode, message, idempotencyKey, idempotencyHash string, now time.Time) error {
	payload := map[string]any{"requirement_id": requirementID, "evidence_id": evidenceID, "validator_principal": validator, "builder_principals": builders, "reason_code": reasonCode, "reason": message}
	mergePayload(payload, idempotencyPayload("task_completion_review", idempotencyKey, idempotencyHash))
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.completion_review_rejected", Actor: validator, Message: "Completion review rejected", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.publish(event)
	return fmt.Errorf("%w: %s", ErrValidation, message)
}

func isRequirementKind(kind model.CompletionRequirementKind) bool {
	switch kind {
	case model.RequirementPullRequest, model.RequirementGreenCI, model.RequirementValidation, model.RequirementResolvedReview, model.RequirementDeployment, model.RequirementHumanApproval, model.RequirementCustom:
		return true
	}
	return false
}
func requiredReferenceKind(kind model.CompletionRequirementKind) model.ReferenceKind {
	switch kind {
	case model.RequirementPullRequest:
		return model.ReferencePullRequest
	case model.RequirementGreenCI:
		return model.ReferenceCIRun
	case model.RequirementValidation:
		return model.ReferenceScreenshot
	case model.RequirementResolvedReview:
		return model.ReferenceReview
	case model.RequirementDeployment:
		return model.ReferenceDeployment
	}
	return ""
}
