package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	defaultFromName  = "nobody"
	defaultFromEmail = "nobody@kernel.org"
	defaultListen    = "127.0.0.1:9110"
	defaultDraftDir  = "/tmp/lore-reply/drafts"
)

// Config holds the resolved runtime settings.
type Config struct {
	B4Path      string
	GitPath     string
	FromName    string
	FromEmail   string
	Listen      string
	DraftsDir   string
	AutoLoadURL string
	ExitOnIdle  bool
}

// Load parses flags, validates dependencies, and returns the runtime config.
func Load() (Config, error) {
	var (
		b4Path      = flag.String("b4", "", "path to the b4 executable")
		fromName    = flag.String("from-name", defaultFromName, "default sender name")
		fromEmail   = flag.String("from-email", defaultFromEmail, "default sender email")
		listen      = flag.String("listen", defaultListen, "listen address")
		autoLoadURL = flag.String("url", "", "lore URL to open and load automatically on startup")
		exitOnIdle  = flag.Bool("exit-on-idle", false, "stop the server after the page heartbeat disappears")
	)

	flag.Parse()

	resolvedB4, err := resolveExecutable(*b4Path, "b4")
	if err != nil {
		return Config{}, fmt.Errorf("resolve b4: %w", err)
	}

	resolvedGit, err := resolveExecutable("", "git")
	if err != nil {
		return Config{}, fmt.Errorf("resolve git: %w", err)
	}

	if err := os.MkdirAll(defaultDraftDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("create drafts dir: %w", err)
	}

	draftsDir, err := filepath.Abs(defaultDraftDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve drafts dir: %w", err)
	}

	return Config{
		B4Path:      resolvedB4,
		GitPath:     resolvedGit,
		FromName:    *fromName,
		FromEmail:   *fromEmail,
		Listen:      *listen,
		DraftsDir:   draftsDir,
		AutoLoadURL: *autoLoadURL,
		ExitOnIdle:  *exitOnIdle,
	}, nil
}

func resolveExecutable(provided, fallback string) (string, error) {
	candidate := fallback
	if provided != "" {
		candidate = provided
	}

	resolved, err := exec.LookPath(candidate)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%q not found", candidate)
		}
		return "", err
	}

	absPath, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}

	return absPath, nil
}
