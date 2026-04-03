package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseIssueConfig_Full(t *testing.T) {
	body := `Some issue text here.

<!-- issuebot
strategy: commit
branch: my-feature
-->

More text.`

	cfg := ParseIssueConfig(body)
	assert.Equal(t, StrategyCommit, cfg.Strategy)
	assert.Equal(t, "my-feature", cfg.Branch)
}

func TestParseIssueConfig_Empty(t *testing.T) {
	body := "Just a normal issue body with no config block."

	cfg := ParseIssueConfig(body)
	assert.Empty(t, cfg.Strategy)
	assert.Empty(t, cfg.Branch)
}

func TestParseIssueConfig_PartialFields(t *testing.T) {
	body := `<!-- issuebot
strategy: worktree
-->`

	cfg := ParseIssueConfig(body)
	assert.Equal(t, "worktree", cfg.Strategy)
	assert.Empty(t, cfg.Branch)
}

func TestParseIssueConfig_UnknownKeysIgnored(t *testing.T) {
	body := `<!-- issuebot
strategy: pr
branch: fix-123
unknown_key: some_value
another: thing
-->`

	cfg := ParseIssueConfig(body)
	assert.Equal(t, StrategyPR, cfg.Strategy)
	assert.Equal(t, "fix-123", cfg.Branch)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultCLIConfig()

	tests := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"strategy", cfg.Strategy, StrategyCommit},
		{"interval", cfg.Interval, 30 * time.Second},
		{"workers", cfg.Workers, 5},
		{"max-retries", cfg.MaxRetries, 3},
		{"label", cfg.Label, ""},
		{"workspace-has-issuebot", strings.Contains(cfg.Workspace, ".issuebot/repos"), true},
		{"workspace-no-tilde", strings.HasPrefix(cfg.Workspace, "~"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.got)
		})
	}
}

func TestParseIssueConfig_MalformedYAML(t *testing.T) {
	body := `<!-- issuebot
strategy: [broken
-->`

	cfg := ParseIssueConfig(body)
	assert.Empty(t, cfg.Strategy)
}

func TestDefaultConfig_WorkspaceUsesHomeDir(t *testing.T) {
	cfg := DefaultCLIConfig()
	home, _ := os.UserHomeDir()

	expected := filepath.Join(home, ".issuebot", "repos")
	assert.Equal(t, expected, cfg.Workspace)
}

func TestResolveStrategy_IssueOverrides(t *testing.T) {
	cli := DefaultCLIConfig()
	issue := IssueConfig{Strategy: StrategyCommit}
	assert.Equal(t, StrategyCommit, ResolveStrategy(cli, issue))
}

func TestResolveStrategy_FallbackToCLI(t *testing.T) {
	cli := DefaultCLIConfig()
	issue := IssueConfig{}
	assert.Equal(t, StrategyCommit, ResolveStrategy(cli, issue))
}

func TestResolveBranch_IssueOverrides(t *testing.T) {
	issue := IssueConfig{Branch: "custom-branch"}
	assert.Equal(t, "custom-branch", ResolveBranch(issue, 42))
}

func TestResolveBranch_Default(t *testing.T) {
	issue := IssueConfig{}
	assert.Equal(t, "issuebot/issue-42", ResolveBranch(issue, 42))
}

func TestParseIssueConfig_PostCommand(t *testing.T) {
	body := "<!-- issuebot\npost-command: gh pr comment $PR_NUMBER -b 'ready'\n-->"
	cfg := ParseIssueConfig(body)
	assert.Equal(t, "gh pr comment $PR_NUMBER -b 'ready'", cfg.PostCommand)
}

func TestParseIssueConfig_PostCommandEmpty(t *testing.T) {
	body := "<!-- issuebot\nstrategy: pr\n-->"
	cfg := ParseIssueConfig(body)
	assert.Empty(t, cfg.PostCommand)
}
