package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func (s *Service) ListEscalationsFor(ctx context.Context, taskID string, principal Principal) ([]model.TaskEscalation, error) {
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && (!principal.HasCapability(CapabilityTaskEscalate) || !canMutate(task, principal)) {
		return nil, ErrForbidden
	}
	return s.store.ListTaskEscalations(ctx, task.ID)
}

func (s *Service) CreateEscalationFor(ctx context.Context, taskID string, request model.CreateEscalationRequest, principal Principal) (model.TaskEscalation, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskEscalate) || !principal.HasCapability(CapabilityTaskMessage) {
		return model.TaskEscalation{}, ErrForbidden
	}
	taskID, request.RunID = strings.TrimSpace(taskID), strings.TrimSpace(request.RunID)
	request.Question, request.Recommendation = strings.TrimSpace(request.Question), strings.TrimSpace(request.Recommendation)
	if taskID == "" || request.RunID == "" || request.ExpectedVersion < 1 {
		return model.TaskEscalation{}, fmt.Errorf("%w: task_id, run_id, and expected_version are required", ErrValidation)
	}
	if request.Question == "" || len(request.Question) > 4000 {
		return model.TaskEscalation{}, fmt.Errorf("%w: question must contain 1-4000 characters", ErrValidation)
	}
	if len(request.Recommendation) > 1000 || len(request.Options) > 10 {
		return model.TaskEscalation{}, fmt.Errorf("%w: recommendation is limited to 1000 characters and choices to 10", ErrValidation)
	}
	seen := map[string]bool{}
	for index := range request.Options {
		request.Options[index] = strings.TrimSpace(request.Options[index])
		key := strings.ToLower(request.Options[index])
		if request.Options[index] == "" || len(request.Options[index]) > 200 || seen[key] {
			return model.TaskEscalation{}, fmt.Errorf("%w: choices must be unique and contain 1-200 characters", ErrValidation)
		}
		seen[key] = true
	}
	answerers, err := normalizeAnswerers(request.Answerers)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	request.Answerers = answerers
	if request.ExpiresIn != 0 && (request.ExpiresIn < 60 || request.ExpiresIn > 7*24*60*60) {
		return model.TaskEscalation{}, fmt.Errorf("%w: expires_in_seconds must be between 60 and 604800", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if !canMutate(task, principal) {
		return model.TaskEscalation{}, ErrForbidden
	}
	if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
		return model.TaskEscalation{}, fmt.Errorf("%w: escalation requires this agent's active run", ErrValidation)
	}

	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	hash, err := requestHash(struct {
		TaskID string
		model.CreateEscalationRequest
	}{taskID, request})
	if err != nil {
		return model.TaskEscalation{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_escalation_create", request.IdempotencyKey, hash)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if replay {
		id, _ := event.Payload["escalation_id"].(string)
		return s.store.GetTaskEscalation(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if current.Version != request.ExpectedVersion || current.Status != model.TaskActive {
		return model.TaskEscalation{}, ErrConflict
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE id=? AND task_id=? AND agent=? AND status=? AND ended_at IS NULL`, request.RunID, taskID, principal.ID, model.TaskActive).Scan(&active); err != nil {
		return model.TaskEscalation{}, err
	}
	if active != 1 {
		return model.TaskEscalation{}, fmt.Errorf("%w: escalation requires this agent's active run", ErrValidation)
	}
	question := model.TaskMessage{ID: newID(now), TaskID: taskID, Author: principal.ID, AuthorRunID: request.RunID, Kind: model.MessageQuestion, Body: request.Question, CreatedAt: now}
	if err := store.InsertTaskMessage(ctx, tx, question); err != nil {
		return model.TaskEscalation{}, err
	}
	escalation := model.TaskEscalation{ID: newID(now), TaskID: taskID, RunID: request.RunID, QuestionMessageID: question.ID, Blocking: request.Blocking, Options: request.Options, Recommendation: request.Recommendation, Status: model.EscalationOpen, Answerers: request.Answerers, CreatedAt: now}
	if request.ExpiresIn > 0 {
		expires := now.Add(time.Duration(request.ExpiresIn) * time.Second)
		escalation.ExpiresAt = &expires
	}
	if err := store.InsertTaskEscalation(ctx, tx, escalation); err != nil {
		return model.TaskEscalation{}, err
	}
	version := current.Version
	if request.Blocking {
		result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,lease_expires_at=?,last_heartbeat_at=?,ended_at=? WHERE id=? AND task_id=? AND status=? AND ended_at IS NULL`, model.TaskWaiting, stamp(now), stamp(now), stamp(now), request.RunID, taskID, model.TaskActive)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.TaskEscalation{}, ErrConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,waiting_for=?,version=version+1,updated_at=? WHERE id=? AND version=? AND status=?`, model.TaskWaiting, clipped(request.Question, 1000), stamp(now), taskID, current.Version, model.TaskActive)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.TaskEscalation{}, ErrConflict
		}
		version++
	}
	payload := map[string]any{"escalation_id": escalation.ID, "question_message_id": question.ID, "blocking": request.Blocking, "status": map[bool]model.TaskStatus{true: model.TaskWaiting, false: model.TaskActive}[request.Blocking], "version": version}
	mergePayload(payload, idempotencyPayload("task_escalation_create", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.escalated", Actor: principal.ID, Message: "Agent requested a decision", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, created); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskEscalation{}, err
	}
	s.publish(created)
	return escalation, nil
}

// ResolveEscalationFor records a person's answer made in Taskboard.
func (s *Service) ResolveEscalationFor(ctx context.Context, taskID, escalationID string, request model.ResolveEscalationRequest, principal Principal) (model.TaskEscalation, error) {
	return s.resolveEscalation(ctx, taskID, escalationID, request, principal, "")
}

// SetAnswerDelegation lets the listed service principals forward answers for
// people, who receive role when acting through them. Delegation applies only
// to escalations that name their answerers.
func (s *Service) SetAnswerDelegation(principals []string, role Role) {
	s.answerDelegates = append([]string(nil), principals...)
	s.answerDelegateRole = role
}

// ResolveEscalationOnBehalfFor records an answer that an allowlisted service,
// such as a notification client, collected from the person personID after
// authenticating them. The escalation must name that person as an answerer;
// the person is recorded as the author and the service as the delegate.
func (s *Service) ResolveEscalationOnBehalfFor(ctx context.Context, taskID, escalationID, personID string, request model.ResolveEscalationRequest, delegate Principal) (model.TaskEscalation, error) {
	if !delegate.Agent || !slices.Contains(s.answerDelegates, delegate.ID) {
		return model.TaskEscalation{}, ErrForbidden
	}
	personID = strings.TrimSpace(personID)
	if !validPersonID(personID) {
		return model.TaskEscalation{}, fmt.Errorf("%w: on_behalf_of must be a person principal", ErrValidation)
	}
	revoked, err := s.store.PrincipalRevoked(ctx, personID)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if revoked {
		return model.TaskEscalation{}, ErrForbidden
	}
	escalation, err := s.store.GetTaskEscalation(ctx, strings.TrimSpace(escalationID))
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if len(escalation.Answerers) == 0 {
		return model.TaskEscalation{}, fmt.Errorf("%w: delegated answers require an escalation that names its answerers", ErrForbidden)
	}
	return s.resolveEscalation(ctx, taskID, escalationID, request, HumanPrincipalWithRole(personID, s.answerDelegateRole), delegate.ID)
}

func (s *Service) resolveEscalation(ctx context.Context, taskID, escalationID string, request model.ResolveEscalationRequest, principal Principal, delegatedBy string) (model.TaskEscalation, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.TaskEscalation{}, ErrForbidden
	}
	taskID, escalationID = strings.TrimSpace(taskID), strings.TrimSpace(escalationID)
	request.Answer, request.SelectedOption = strings.TrimSpace(request.Answer), strings.TrimSpace(request.SelectedOption)
	if request.Answer == "" {
		// A selected choice is a complete answer; it is validated against the
		// escalation's choices below.
		request.Answer = request.SelectedOption
	}
	if taskID == "" || escalationID == "" || request.ExpectedVersion < 1 || request.Answer == "" || len(request.Answer) > 4000 {
		return model.TaskEscalation{}, fmt.Errorf("%w: task_id, escalation_id, expected_version, and a selected_option or a 1-4000 character answer are required", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if !canMutate(task, principal) {
		return model.TaskEscalation{}, ErrForbidden
	}

	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID       string
		EscalationID string
		DelegatedBy  string
		model.ResolveEscalationRequest
	}{taskID, escalationID, delegatedBy, request})
	if err != nil {
		return model.TaskEscalation{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_escalation_resolve", request.IdempotencyKey, hash)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if replay {
		id, _ := event.Payload["escalation_id"].(string)
		return s.store.GetTaskEscalation(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if current.Version != request.ExpectedVersion {
		return model.TaskEscalation{}, ErrConflict
	}
	escalation, err := store.LoadTaskEscalation(ctx, tx, escalationID)
	if err != nil || escalation.TaskID != taskID {
		return model.TaskEscalation{}, store.ErrNotFound
	}
	if escalation.Status == model.EscalationExpired {
		return model.TaskEscalation{}, fmt.Errorf("%w: escalation expired; the agent must ask again", ErrValidation)
	}
	if escalation.Status != model.EscalationOpen {
		return model.TaskEscalation{}, ErrConflict
	}
	if len(escalation.Answerers) > 0 && !slices.Contains(escalation.Answerers, principal.ID) {
		return model.TaskEscalation{}, fmt.Errorf("%w: this escalation can only be answered by its named answerers", ErrForbidden)
	}
	if request.SelectedOption != "" && !containsExact(escalation.Options, request.SelectedOption) {
		return model.TaskEscalation{}, fmt.Errorf("%w: selected_option must match one of the escalation choices", ErrValidation)
	}
	if escalation.Blocking && current.Status != model.TaskWaiting {
		return model.TaskEscalation{}, fmt.Errorf("%w: blocking escalation can only be answered while the task is waiting", ErrValidation)
	}
	answer := model.TaskMessage{ID: newID(now), TaskID: taskID, Author: principal.ID, Kind: model.MessageAnswer, Body: request.Answer, ReplyToID: escalation.QuestionMessageID, CreatedAt: now}
	if err := store.InsertTaskMessage(ctx, tx, answer); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := store.ResolveTaskEscalation(ctx, tx, escalationID, answer.ID, request.SelectedOption, principal.ID, delegatedBy, now); err != nil {
		return model.TaskEscalation{}, err
	}
	version := current.Version
	if escalation.Blocking {
		owner := current.Owner
		if current.Visibility == model.VisibilityAgent {
			owner = ""
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,owner=?,waiting_for='',version=version+1,updated_at=? WHERE id=? AND version=? AND status=?`, model.TaskQueued, owner, stamp(now), taskID, current.Version, model.TaskWaiting)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.TaskEscalation{}, ErrConflict
		}
		version++
	}
	payload := map[string]any{"escalation_id": escalation.ID, "answer_message_id": answer.ID, "blocking": escalation.Blocking, "status": map[bool]model.TaskStatus{true: model.TaskQueued, false: current.Status}[escalation.Blocking], "version": version}
	if delegatedBy != "" {
		payload["delegated_by"] = delegatedBy
	}
	mergePayload(payload, idempotencyPayload("task_escalation_resolve", request.IdempotencyKey, request.IdempotencyHash))
	resolved := model.Event{ID: newID(now), TaskID: taskID, RunID: escalation.RunID, Kind: "task.escalation_answered", Actor: principal.ID, Message: "Escalation answered", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, resolved); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskEscalation{}, err
	}
	s.publish(resolved)
	return s.store.GetTaskEscalation(ctx, escalationID)
}

// MarkDecisionsFor sets DecisionRequested on tasks with an open, unexpired
// escalation the person may answer: one that names them, or names nobody.
func (s *Service) MarkDecisionsFor(ctx context.Context, tasks []model.Task, principal Principal) error {
	if principal.Agent || !principal.Can(PermissionTaskWrite) || len(tasks) == 0 {
		return nil
	}
	open, err := s.store.ListOpenTaskEscalations(ctx)
	if err != nil {
		return err
	}
	waiting := map[string]bool{}
	for _, escalation := range open {
		if escalation.Status == model.EscalationOpen && (len(escalation.Answerers) == 0 || slices.Contains(escalation.Answerers, principal.ID)) {
			waiting[escalation.TaskID] = true
		}
	}
	for index := range tasks {
		tasks[index].DecisionRequested = waiting[tasks[index].ID]
	}
	return nil
}

// normalizeAnswerers validates the people allowed to answer. Agents and
// service tokens cannot answer, so naming them would be meaningless.
func normalizeAnswerers(values []string) ([]string, error) {
	if len(values) > 20 {
		return nil, fmt.Errorf("%w: answerers are limited to 20", ErrValidation)
	}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validPersonID(value) {
			return nil, fmt.Errorf("%w: answerers must be person principal IDs", ErrValidation)
		}
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result, nil
}

func validPersonID(value string) bool {
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, " \t\r\n") &&
		!strings.HasPrefix(value, "agent:") && !strings.HasPrefix(value, "cloudflare_access:service_token:")
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
