package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/spf13/cobra"
	"github.com/tinybluerobots/issuebot/internal/cache"
	"github.com/tinybluerobots/issuebot/internal/config"
	"github.com/tinybluerobots/issuebot/internal/notify"
	"github.com/tinybluerobots/issuebot/internal/poller"
	"github.com/tinybluerobots/issuebot/internal/prompt"
	"github.com/tinybluerobots/issuebot/internal/state"
	"github.com/tinybluerobots/issuebot/internal/worker"
	"golang.org/x/oauth2"
)

var (
	// ErrNoGitHubToken is returned when no GitHub token is available.
	ErrNoGitHubToken = errors.New("no GitHub token found (set GITHUB_TOKEN or run 'gh auth login')")
	// ErrNoRepo is returned when no org/repo is specified and detection fails.
	ErrNoRepo = errors.New("no --org or --repo specified and not in a git repo")
)

var cfg = config.DefaultCLIConfig()

func init() {
	f := rootCmd.Flags()
	f.StringVar(&cfg.Org, "org", cfg.Org, "GitHub organization")
	f.StringVar(&cfg.Repo, "repo", cfg.Repo, "GitHub repository")
	f.StringVar(&cfg.Label, "label", cfg.Label, "Issue label to watch")
	f.StringVar(&cfg.Author, "author", cfg.Author, "Only process issues by this GitHub username")
	f.StringVar(&cfg.Strategy, "strategy", cfg.Strategy, "Default strategy (pr, commit)")
	f.DurationVar(&cfg.Interval, "interval", cfg.Interval, "Poll interval")
	f.IntVar(&cfg.Workers, "workers", cfg.Workers, "Max concurrent workers")
	f.IntVar(&cfg.MaxRetries, "max-retries", cfg.MaxRetries, "Max retries per issue")
	f.StringVar(&cfg.Workspace, "workspace", cfg.Workspace, "Workspace directory for repo clones")
	f.BoolVar(&cfg.Local, "local", cfg.Local, "Use current directory instead of cloning")
	f.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "Run command but skip push/PR (print diff instead)")
	f.StringVar(&cfg.Command, "command", cfg.Command, "Command to run ({prompt} is replaced with the rendered prompt)")

	_ = rootCmd.MarkFlagRequired("command")

	f.StringVar(&cfg.PromptFile, "prompt-file", cfg.PromptFile, "Path to prompt template file")
	f.StringVar(&cfg.LogFile, "log-file", cfg.LogFile, "Log file path")
	f.StringVar(&cfg.NtfyTopic, "ntfy-topic", cfg.NtfyTopic, "ntfy.sh topic for notifications")
}

func githubToken() string {
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok
	}

	out, err := exec.CommandContext(context.Background(), "gh", "auth", "token").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}

	return ""
}

func runWatch(cmd *cobra.Command, args []string) error {
	// Logging
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}

	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}

		defer func() { _ = f.Close() }()

		slog.SetDefault(slog.New(slog.NewTextHandler(f, opts)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
	}

	// Notifications
	ntfyTopic := cfg.NtfyTopic
	if ntfyTopic == "" {
		ntfyTopic = os.Getenv("NTFY_TOPIC")
	}

	notifier := notify.New(ntfyTopic)

	// GitHub client
	token := githubToken()
	if token == "" {
		return ErrNoGitHubToken
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	oauthClient := oauth2.NewClient(context.Background(), ts)
	oauthClient.Transport = &cache.Transport{Base: oauthClient.Transport}
	ghClient := github.NewClient(oauthClient)

	// Resolve repo from current dir if needed
	if cfg.Org == "" && cfg.Repo == "" {
		out, err := exec.CommandContext(context.Background(), "gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").Output()
		if err != nil {
			return ErrNoRepo
		}

		cfg.Repo = strings.TrimSpace(string(out))
	}

	// State
	home, _ := os.UserHomeDir()

	stateDir := home + "/.issuebot"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}

	st, err := state.Load(stateDir + "/state.json")
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	st.RecoverCrashed()

	if err := st.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Workspace
	if err := os.MkdirAll(cfg.Workspace, 0755); err != nil {
		return fmt.Errorf("mkdir workspace: %w", err)
	}

	// Prompt template
	if err := prompt.EnsureFile(cfg.PromptFile); err != nil {
		return fmt.Errorf("ensure prompt file: %w", err)
	}

	slog.Info("using prompt", "file", cfg.PromptFile)

	// Poller
	p := &poller.Poller{
		Client:     ghClient,
		Org:        cfg.Org,
		SingleRepo: cfg.Repo,
		Label:      cfg.Label,
		Author:     cfg.Author,
	}

	// Signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	sem := make(chan struct{}, cfg.Workers)

	var repoLocks sync.Map

	target := "current repo"
	if cfg.Org != "" {
		target = fmt.Sprintf("org '%s'", cfg.Org)
	} else if cfg.Repo != "" {
		target = cfg.Repo
	}

	slog.Info("starting issuebot",
		"target", target, "label", cfg.Label,
		"strategy", cfg.Strategy, "interval", cfg.Interval,
		"workers", cfg.Workers,
	)

	poll := func() {
		repos, err := p.ListRepos(ctx)
		if err != nil {
			slog.Error("list repos", "error", err)
			return
		}

		for _, repo := range repos {
			issues, err := p.ListIssues(ctx, repo)
			if err != nil {
				slog.Error("list issues", "repo", repo, "error", err)
				continue
			}

			for _, issue := range issues {
				key := fmt.Sprintf("%s#%d", repo, issue.GetNumber())
				if !st.ShouldProcess(key, cfg.MaxRetries) {
					continue
				}

				lockI, _ := repoLocks.LoadOrStore(repo, &sync.Mutex{})

				repoMu := lockI.(*sync.Mutex)
				if !repoMu.TryLock() {
					continue
				}

				sem <- struct{}{}

				go func() {
					defer func() {
						<-sem
						repoMu.Unlock()
					}()

					w := &worker.Worker{
						Client:    ghClient,
						State:     st,
						Notifier:  notifier,
						CLIConfig: cfg,
					}
					w.ProcessIssue(ctx, repo, issue)
				}()
			}
		}
	}

	// Run immediately, then on interval
	poll()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return nil
		case <-ticker.C:
			poll()
		}
	}
}
