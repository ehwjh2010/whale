package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/session"
	"github.com/usewhale/whale/internal/store"
)

type ResumeRejectedError struct {
	Reason string
}

func (e *ResumeRejectedError) Error() string {
	if e == nil {
		return ""
	}
	return e.Reason
}

func IsResumeRejectedError(err error) bool {
	var target *ResumeRejectedError
	return errors.As(err, &target)
}

type InvalidModeError struct {
	Value string
}

func (e *InvalidModeError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("invalid mode: %q (supported: agent, ask, plan)", e.Value)
}

func IsInvalidModeError(err error) bool {
	var target *InvalidModeError
	return errors.As(err, &target)
}

func ValidateResumeTarget(cfg Config, start StartOptions, currentWorkspace string) (WorktreeSession, error) {
	if start.NewSession || strings.TrimSpace(start.SessionID) == "" {
		return WorktreeSession{}, nil
	}
	sessionsDir := store.DefaultSessionsDir(cfg.DataDir)
	if _, err := session.ResolveStrictSession(sessionsDir, start.SessionID); err != nil {
		return WorktreeSession{}, &ResumeRejectedError{Reason: err.Error()}
	}
	if msg, blocked, err := CheckResumeWorkspace(sessionsDir, start.SessionID, currentWorkspace); err != nil {
		return WorktreeSession{}, err
	} else if blocked {
		return WorktreeSession{}, &CrossWorkspaceResumeError{Message: msg}
	}
	decision, err := ResolveResumeWorktreeDecision(cfg, start, currentWorkspace)
	if err != nil {
		return WorktreeSession{}, err
	}
	explicit := strings.TrimSpace(start.Worktree.Path)
	switch {
	case decision.Session.Path != "":
		if explicit != "" && explicit != decision.Session.Path {
			return WorktreeSession{}, &ResumeRejectedError{Reason: fmt.Sprintf("explicit worktree %q does not match session record %q", explicit, decision.Session.Path)}
		}
		return decision.Session, nil
	case decision.MissingWorktree:
		if explicit != "" {
			return WorktreeSession{}, &ResumeRejectedError{Reason: fmt.Sprintf("session worktree is gone; explicit worktree %q cannot take over session ownership", explicit)}
		}
		return WorktreeSession{}, nil
	default:
		if explicit != "" {
			return WorktreeSession{}, &ResumeRejectedError{Reason: fmt.Sprintf("session %q has no worktree record; explicit worktree %q rejected", start.SessionID, explicit)}
		}
		return WorktreeSession{}, nil
	}
}

func CommitStartState(cfg Config, start StartOptions, currentWorkspace string) error {
	if start.NewSession || strings.TrimSpace(start.SessionID) == "" {
		return nil
	}
	sessionID := strings.TrimSpace(start.SessionID)
	decision, err := ResolveResumeWorktreeDecision(cfg, start, currentWorkspace)
	if err != nil {
		return err
	}
	if decision.MissingWorktree {
		if err := CommitMissingWorktreeCleanup(store.DefaultSessionsDir(cfg.DataDir), sessionID, currentWorkspace); err != nil {
			return err
		}
	}
	if raw := strings.TrimSpace(start.ModeOverride); raw != "" {
		mode, err := session.ParseMode(raw)
		if err != nil {
			return &InvalidModeError{Value: raw}
		}
		if err := session.SaveModeState(store.DefaultSessionsDir(cfg.DataDir), sessionID, mode); err != nil {
			return fmt.Errorf("save mode state failed: %w", err)
		}
	}
	return nil
}
