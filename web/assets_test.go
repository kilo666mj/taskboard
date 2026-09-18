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
	for _, expected := range []string{`run.callsign`, `'Principal',run.agent`, `'Client',run.client`, `'Run ID',run.id`, `setAttribute('aria-label',`} {
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
