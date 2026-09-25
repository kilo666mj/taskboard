package webassets

import (
	"strings"
	"testing"
)

func embeddedText(t *testing.T, name string) string {
	t.Helper()
	content, err := Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestAgentIdentityUIKeepsTextAndTrustedDetails(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	for _, expected := range []string{`<details class="run-details"`, `<summary>Agent sessions</summary>`} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{`run.callsign`, `'Principal',run.agent`, `'Client',run.client`, `'Agent session ID',run.session_id`, `'Task run ID',run.id`, `setAttribute('aria-label',`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
}

func TestAgentIdentityAndDesktopToolbarHaveResponsiveStyles(t *testing.T) {
	css := embeddedText(t, "app.css")
	for _, expected := range []string{
		`.agent-chip[data-tone="0"]`,
		`.agent-chip[data-tone="7"]`,
		`.agent-chip-label`,
		`html:not([data-runtime="desktop"]) .view-picker select`,
		`.view-picker select{appearance:none;height:34px`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css missing %q", expected)
		}
	}
}

func TestEscalationDecisionUIIsWiredAndResponsive(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	css := embeddedText(t, "app.css")
	for _, expected := range []string{`class="escalation-list"`, `aria-label="Task decisions"`} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{`function renderEscalations`, `/escalations/${escalation.id}/answer`, `expected_version:task.version`, `textarea.required=true`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
	for _, expected := range []string{`.escalation[data-blocking=true]`, `.send-message,.escalation-answer-form .primary,.request-control,.add-reference,.add-completion`} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css missing %q", expected)
		}
	}
}

func TestRunControlUIShowsRequestsAsAcknowledgedWorkflow(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	css := embeddedText(t, "app.css")
	for _, expected := range []string{`class="control-panel"`, `class="control-list"`, `class="control-form"`} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{`function availableControl`, `function renderControls`, `/controls`, `data.control`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
	for _, expected := range []string{`.control-status`, `.send-message,.escalation-answer-form .primary,.request-control,.add-reference,.add-completion`} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css missing %q", expected)
		}
	}
}

func TestStaleRecoveryIsOperatorOnlyAndRecordsReview(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	for _, expected := range []string{`class="requeue-help field-help"`, `Review & requeue`, `records your identity as the last editor`} {
		if !strings.Contains(html, expected) && !strings.Contains(javascript, expected) {
			t.Errorf("recovery UI missing %q", expected)
		}
	}
	for _, expected := range []string{`function canOperateTasks`, `/review-requeue`, `review_note:`, `task.status==='stale'`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
}

func TestDeliveryUIUsesTypedReferencesAndHTTPSLinks(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	css := embeddedText(t, "app.css")
	for _, expected := range []string{`<details class="delivery">`, `class="milestone-list"`, `class="reference-list"`, `class="reference-form"`} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{`function safeReferenceURL`, `parsed.protocol==='https:'`, `rel='noopener noreferrer'`, `/delivery`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
	for _, expected := range []string{`.milestone-list`, `.reference-kind`, `.send-message,.escalation-answer-form .primary,.request-control,.add-reference,.add-completion`} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css missing %q", expected)
		}
	}
}

func TestControlPlaneExpansionUIIsWiredAndResponsive(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	css := embeddedText(t, "app.css")
	for _, expected := range []string{
		`class="handoff-list"`,
		`class="completion-contract"`,
		`<details class="dependency-panel" hidden>`,
		`<summary>Blocked by</summary>`,
		`<details class="requirements-panel" hidden>`,
		`<summary>Worker requirements</summary>`,
		`id="analyticsDialog"`,
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{
		`function renderCompletion`,
		`function renderDependencies`,
		`function renderRequirements`,
		`/session-requests`,
		`/api/v1/analytics?days=`,
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
	for _, expected := range []string{
		`.session-actions`,
		`.analytics-grid`,
		`.dependency-form,.requirements-form{grid-template-columns:1fr}`,
		`.reference,.handoff,.completion-requirement,.dependency{grid-template-columns:1fr}`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css missing %q", expected)
		}
	}
}

func TestTaskCancellationUsesAcknowledgedControlsForActiveRuns(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	css := embeddedText(t, "app.css")
	for _, expected := range []string{`class="text-button cancel-task"`, `>Cancel task</button>`} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{
		`function cancelTaskFromCard`,
		`run.status==='active'&&!run.ended_at`,
		`kind:'cancel'`,
		`status:'cancelled'`,
		`window.confirm(prompt)`,
	} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
	for _, expected := range []string{`.cancel-task{margin-left:auto`, `.task-card[data-status=cancelled] .cancel-task{display:none}`} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css missing %q", expected)
		}
	}
}

func TestTaskContextExplainsDurableDescriptionAndConversation(t *testing.T) {
	html := embeddedText(t, "index.html")
	for _, expected := range []string{
		`Task context <span>(durable)</span>`,
		`rows="4" aria-describedby="taskContextHelp"`,
		`Use Conversation for later updates and decisions`,
		`class="task-context" aria-label="Task context"`,
	} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	javascript := embeddedText(t, "app.js")
	if !strings.Contains(javascript, `function renderTaskContext`) {
		t.Error("app.js does not render the full task context")
	}
}

func TestTaskViewsAndSortControlAreWired(t *testing.T) {
	html := embeddedText(t, "index.html")
	javascript := embeddedText(t, "app.js")
	for _, expected := range []string{`<option value="mine">My tasks</option>`, `<option value="active">Active</option>`, `<option value="agents">Agents running</option>`, `<select id="sortOrder"`} {
		if !strings.Contains(html, expected) {
			t.Errorf("index.html missing %q", expected)
		}
	}
	for _, expected := range []string{`function isMine`, `function compareTasks`, `sort(compareTasks)`, `task.visibility==='agent'&&task.ready`, `!manualOrder`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
}

func TestFixedTaskTypeHidesTypeSelector(t *testing.T) {
	javascript := embeddedText(t, "app.js")
	css := embeddedText(t, "app.css")
	for _, expected := range []string{`state.taskType=session.task_type||''`, `$('#taskType').closest('label').hidden=Boolean(state.taskType)`, `if(!state.taskType)labels.push(`, `...(state.taskType?{}:{type:`} {
		if !strings.Contains(javascript, expected) {
			t.Errorf("app.js missing %q", expected)
		}
	}
	if !strings.Contains(css, `.modal label[hidden]{display:none}`) {
		t.Error("app.css does not hide a hidden dialog label")
	}
}
