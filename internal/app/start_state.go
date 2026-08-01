package app

import (
	"strings"

	"github.com/usewhale/whale/internal/store"
)

func CommitStartState(cfg Config, start StartOptions, currentWorkspace string) error {
	if start.NewSession || strings.TrimSpace(start.SessionID) == "" {
		return nil
	}
	decision, err := ResolveResumeWorktreeDecision(cfg, start, currentWorkspace)
	if err != nil {
		return err
	}
	if decision.MissingWorktree {
		return CommitMissingWorktreeCleanup(store.DefaultSessionsDir(cfg.DataDir), strings.TrimSpace(start.SessionID), currentWorkspace)
	}
	return nil
}
