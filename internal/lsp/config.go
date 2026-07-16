// Package lsp provides a Language Server Protocol client and server manager.
//
// # Overview
//
// The LSP subsystem enables code intelligence tools — go-to-definition,
// find-references, hover, document symbols, and workspace symbol search —
// by starting and managing language servers as subprocesses.
//
// # Configuration
//
// Configuration is loaded from the data directory's lsp.json file
// (DefaultConfigPath). The file is optional; if absent, built-in defaults
// are used for common languages (Go, Python, TypeScript, Rust, C/C++,
// HTML/CSS/JSON, YAML, Vue).  The defaults only activate when the
// corresponding language server binary is found on PATH or in known install
// directories.
//
// # Custom Configuration (lsp.json)
//
// Create lsp.json in ~/.whale/ (or the configured data directory) to
// override defaults or add new servers:
//
//	{
//	  "servers": {
//	    "go": {
//	      "command": "gopls",
//	      "args": ["-remote=auto"],
//	      "extensionToLanguage": {".go": "go"}
//	    },
//	    "python": {
//	      "command": "pyright",
//	      "args": ["--stdio"],
//	      "extensionToLanguage": {".py": "python", ".pyi": "python"},
//	      "install_help": "pip install pyright"
//	    },
//	    "custom-lang": {
//	      "command": "my-lang-server",
//	      "args": ["--stdio"],
//	      "extensionToLanguage": {".myext": "my-lang"},
//	      "install_help": "see https://example.com/install"
//	    }
//	  }
//	}
//
// ServerConfig fields:
//   - command: executable name or path (required)
//   - args: CLI arguments forwarded to the server
//   - extensionToLanguage: maps file extensions to language IDs (required)
//   - install_help: shown when the binary is not found
//   - env: environment variables injected into the server process
//   - initializationOptions / settings: LSP initialize options
//   - startupTimeoutMS / shutdownTimeoutMS: timeout overrides
//   - restartOnCrash / maxRestarts: crash recovery
//
// # Discovery
//
// The manager locates server binaries via:
//  1. exec.LookPath (standard PATH search)
//  2. Known install directories (Go bin, Cargo bin, npm global, LLVM)
//  3. VS Code extension directories (pyright from Pylance, rust-analyzer, etc.)
//
// # Lifecycle
//
// Language servers are started lazily on first tool invocation and warmed
// up in the background when workspace files with matching extensions are
// detected.  The manager tracks per-server state (loaded / indexing /
// failed / not_installed) and exposes it via AvailableSummary.
//
// # Tool Exposure
//
// The internal/tools package registers nine LSP tools when a manager is
// available: lsp_goto_definition, lsp_find_references, lsp_hover,
// lsp_document_symbol, lsp_workspace_symbol, lsp_go_to_implementation,
// lsp_prepare_call_hierarchy, lsp_incoming_calls, and lsp_outgoing_calls.
// Each tool requires an "lsp.read" capability and is read-only.
package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultConfigFile = "lsp.json"

// ServerConfig describes a single language server: its executable, arguments,
// file-extension mappings, and behavioral options.
type ServerConfig struct {
	Command               string            `json:"command"`
	Args                  []string          `json:"args,omitempty"`
	ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
	Transport             string            `json:"transport,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	InitializationOptions any               `json:"initializationOptions,omitempty"`
	Settings              any               `json:"settings,omitempty"`
	WorkspaceFolder       string            `json:"workspaceFolder,omitempty"`
	StartupTimeoutMS      int               `json:"startupTimeout,omitempty"`
	ShutdownTimeoutMS     int               `json:"shutdownTimeout,omitempty"`
	RestartOnCrash        *bool             `json:"restartOnCrash,omitempty"`
	MaxRestarts           int               `json:"maxRestarts,omitempty"`
	Diagnostics           *bool             `json:"diagnostics,omitempty"`
	InstallHelp           string            `json:"install_help,omitempty"`
}

func (s *ServerConfig) StartupTimeout() time.Duration {
	if s.StartupTimeoutMS > 0 {
		return time.Duration(s.StartupTimeoutMS) * time.Millisecond
	}
	return 30 * time.Second
}

func (s *ServerConfig) ShutdownTimeout() time.Duration {
	if s.ShutdownTimeoutMS > 0 {
		return time.Duration(s.ShutdownTimeoutMS) * time.Millisecond
	}
	return 5 * time.Second
}

func (s *ServerConfig) ShouldRestartOnCrash() bool {
	if s.RestartOnCrash != nil {
		return *s.RestartOnCrash
	}
	return true
}

func (s *ServerConfig) ShouldPushDiagnostics() bool {
	if s.Diagnostics != nil {
		return *s.Diagnostics
	}
	return true
}

func (s *ServerConfig) Validate() error {
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("missing required field: command")
	}
	if len(s.ExtensionToLanguage) == 0 {
		return fmt.Errorf("missing required field: extensionToLanguage")
	}
	return nil
}

// LSPConfig holds all configured language servers, loaded from lsp.json
// and augmented with built-in defaults.
type LSPConfig struct {
	Servers map[string]*ServerConfig `json:"servers"`
	claimed map[string]string
}

func NewLSPConfig() *LSPConfig {
	return &LSPConfig{
		Servers: make(map[string]*ServerConfig),
		claimed: make(map[string]string),
	}
}

func (c *LSPConfig) RegisterServer(name string, srv *ServerConfig) error {
	if err := srv.Validate(); err != nil {
		return fmt.Errorf("server %q: %w", name, err)
	}
	for ext := range srv.ExtensionToLanguage {
		if owner, ok := c.claimed[ext]; ok {
			return fmt.Errorf("extension %s already claimed by server %q, server %q skipped", ext, owner, name)
		}
		c.claimed[ext] = name
	}
	c.Servers[name] = srv
	return nil
}

func (c *LSPConfig) ForExtension(ext string) (*ServerConfig, string, bool) {
	ext = strings.ToLower(ext)
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if name, ok := c.claimed[ext]; ok {
		return c.Servers[name], name, true
	}
	return nil, "", false
}

func (c *LSPConfig) HasConfiguredServers() bool {
	for _, srv := range c.Servers {
		if _, _, found := FindServerForConfig(srv); found {
			return true
		}
	}
	return false
}

type LegacyFileConfig struct {
	Languages []LegacyLanguage `json:"languages"`
}

type LegacyLanguage struct {
	Name        string   `json:"name"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	Extensions  []string `json:"extensions"`
	InstallHelp string   `json:"install_help,omitempty"`
}

func LoadLSPConfig(path string) (*LSPConfig, error) {
	cfg := NewLSPConfig()
	loadDefaults(cfg)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse lsp config: %w", err)
	}

	userServers := parseUserServers(raw)
	for name, srv := range userServers {
		if existing, ok := cfg.Servers[name]; ok {
			// Remove the default so RegisterServer can claim extensions fresh.
			// Save the default to restore if the user's config is invalid.
			saved := existing
			for ext := range existing.ExtensionToLanguage {
				delete(cfg.claimed, ext)
			}
			delete(cfg.Servers, name)
			if err := cfg.RegisterServer(name, srv); err != nil {
				fmt.Fprintf(os.Stderr, "whale: lsp: skipping server %q from user config: %v (keeping default)\n", name, err)
				cfg.RegisterServer(name, saved)
				continue
			}
		} else {
			if err := cfg.RegisterServer(name, srv); err != nil {
				fmt.Fprintf(os.Stderr, "whale: lsp: skipping server %q from user config: %v\n", name, err)
				continue
			}
		}
	}

	return cfg, nil
}

func parseUserServers(raw map[string]json.RawMessage) map[string]*ServerConfig {
	result := make(map[string]*ServerConfig)

	if serversRaw, ok := raw["servers"]; ok {
		var servers map[string]*ServerConfig
		if err := json.Unmarshal(serversRaw, &servers); err == nil {
			return servers
		} else {
			fmt.Fprintf(os.Stderr, "whale: lsp: parse servers field failed, trying legacy: %v\n", err)
		}
	}

	if langRaw, ok := raw["languages"]; ok {
		var langs []LegacyLanguage
		if err := json.Unmarshal(langRaw, &langs); err == nil {
			for _, lang := range langs {
				result[lang.Name] = legacyToServer(lang)
			}
			return result
		}
	}

	for name, srvRaw := range raw {
		if name == "languages" || name == "servers" {
			continue
		}
		var srv ServerConfig
		if err := json.Unmarshal(srvRaw, &srv); err == nil {
			result[name] = &srv
		}
	}

	return result
}

func legacyToServer(lang LegacyLanguage) *ServerConfig {
	extMap := make(map[string]string)
	for _, ext := range lang.Extensions {
		extMap[ext] = lang.Name
	}
	return &ServerConfig{
		Command:             lang.Command,
		Args:                lang.Args,
		ExtensionToLanguage: extMap,
		InstallHelp:         lang.InstallHelp,
	}
}

func loadDefaults(cfg *LSPConfig) {
	defaults := map[string]*ServerConfig{
		"go": {
			Command:             "gopls",
			ExtensionToLanguage: map[string]string{".go": "go"},
			InstallHelp:         "go install golang.org/x/tools/gopls@latest",
		},
		"rust": {
			Command:             "rust-analyzer",
			ExtensionToLanguage: map[string]string{".rs": "rust"},
			InstallHelp:         "rustup component add rust-analyzer",
		},
		"python": {
			Command: "pyright", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".py": "python", ".pyi": "python"},
			InstallHelp:         "npm install -g pyright",
		},
		"typescript": {
			Command: "typescript-language-server", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".ts": "typescript", ".tsx": "typescriptreact", ".js": "javascript", ".jsx": "javascriptreact", ".mjs": "javascript", ".cjs": "javascript"},
			InstallHelp:         "npm install -g typescript-language-server typescript",
		},
		"html": {
			Command: "vscode-html-language-server", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".html": "html", ".htm": "html"},
			InstallHelp:         "npm install -g vscode-langservers-extracted",
		},
		"css": {
			Command: "vscode-css-language-server", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".css": "css", ".scss": "css", ".less": "css"},
			InstallHelp:         "npm install -g vscode-langservers-extracted",
		},
		"json": {
			Command: "vscode-json-language-server", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".json": "json", ".jsonc": "json"},
			InstallHelp:         "npm install -g vscode-langservers-extracted",
		},
		"cpp": {
			Command:             "clangd",
			ExtensionToLanguage: map[string]string{".c": "c", ".h": "c", ".cpp": "cpp", ".hpp": "cpp", ".cc": "cpp", ".cxx": "cpp", ".hxx": "cpp"},
			InstallHelp:         "Install clangd from https://clangd.llvm.org/installation.html",
		},
		"yaml": {
			Command: "yaml-language-server", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".yaml": "yaml", ".yml": "yaml"},
			InstallHelp:         "npm install -g yaml-language-server",
		},
		"vue": {
			Command: "vue-language-server", Args: []string{"--stdio"},
			ExtensionToLanguage: map[string]string{".vue": "vue"},
			InstallHelp:         "npm install -g @vue/language-server",
		},
	}
	for name, srv := range defaults {
		if err := cfg.RegisterServer(name, srv); err != nil {
			fmt.Fprintf(os.Stderr, "whale: lsp: skipping default server %q: %v\n", name, err)
		}
	}
}

func WriteSampleConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	cfg := NewLSPConfig()
	loadDefaults(cfg)
	data, err := json.MarshalIndent(map[string]any{"servers": cfg.Servers}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func DefaultConfigPath(dataDir string) string {
	return filepath.Join(dataDir, DefaultConfigFile)
}
