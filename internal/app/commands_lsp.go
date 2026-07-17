package app

import (
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/lsp"
)

const lspUsage = "/lsp [status|on|off|sample]"

// LSPStatusInfo returns a human-readable LSP status for the /lsp command.
func (a *App) LSPStatusInfo() string {
	if a == nil {
		return "LSP: off"
	}
	if !a.cfg.LSPEnabled {
		return "LSP: off (default)\n\nEnable with /lsp on — takes effect on the next start.\nServer definitions: " + lsp.DefaultConfigPath(a.cfg.DataDir) + " (create with /lsp sample)"
	}
	if a.lspManager == nil {
		return "LSP: on (no manager in this session — restart to apply, or check startup warnings)"
	}
	summary := a.lspManager.AvailableSummary()
	if summary == "" {
		return "LSP: on (no servers configured)"
	}
	return "LSP: on\n\n" + summary
}

// SetLSPEnabledPersist toggles LSP in the config file so the choice
// survives restarts, and applies what it can to the running session.
func (a *App) SetLSPEnabledPersist(enabled bool) (string, error) {
	path := GlobalConfigPath(a.cfg.DataDir)
	file, _, err := LoadConfigFile(path)
	if err != nil {
		return "", err
	}
	v := enabled
	file.Lsp.Enabled = &v
	if err := SaveConfigFile(path, file); err != nil {
		return "", err
	}
	a.cfg.LSPEnabled = enabled
	if enabled {
		if a.lspManager != nil {
			a.lspManager.Warmup()
			return "LSP enabled, warming up language servers...", nil
		}
		return "LSP enabled — restart whale to start language servers.", nil
	}
	if a.lspManager != nil {
		_ = a.lspManager.Close()
	}
	return "LSP disabled.", nil
}

// LSPWriteSampleConfig writes a default lsp.json server config.
func (a *App) LSPWriteSampleConfig() (string, error) {
	path := lsp.DefaultConfigPath(a.cfg.DataDir)
	if err := lsp.WriteSampleConfig(path); err != nil {
		return "", err
	}
	return "Sample config written to " + path + " — restart whale to apply.", nil
}

func (a *App) executeLSPCommand(trimmed string) (CommandExecution, error) {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/lsp"))
	switch arg {
	case "", "status":
		return CommandExecution{Handled: true, Text: a.LSPStatusInfo()}, nil
	case "on", "off":
		msg, err := a.SetLSPEnabledPersist(arg == "on")
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		return CommandExecution{Handled: true, Text: msg}, nil
	case "sample":
		msg, err := a.LSPWriteSampleConfig()
		if err != nil {
			return CommandExecution{Handled: true}, err
		}
		return CommandExecution{Handled: true, Text: msg}, nil
	default:
		return CommandExecution{Handled: true}, fmt.Errorf("usage: %s", lspUsage)
	}
}
