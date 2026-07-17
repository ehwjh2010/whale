package lsp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func FindServerForConfig(srv *ServerConfig) (string, []string, bool) {
	name := srv.Command
	args := append([]string{}, srv.Args...)

	if path, err := exec.LookPath(name); err == nil {
		// rustup's proxy shim usually sits on PATH (~/.cargo/bin); a hit
		// here must pass the same guard as the install-dir fallback.
		if !isRustupProxy(path) {
			return path, args, true
		}
	}

	for _, dir := range knownInstallDirs() {
		exe := name
		if isWindows() {
			exe += ".exe"
		}
		candidate := filepath.Join(dir, exe)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if isRustupProxy(candidate) {
				continue
			}
			return candidate, args, true
		}
	}

	if path, extArgs, found := vscodeExtensionServer(srv); found {
		// The fallback returns the complete args for the binary it found;
		// appending srv.Args on top would duplicate flags like --stdio.
		if len(extArgs) > 0 {
			return path, extArgs, true
		}
		return path, args, true
	}

	return name, args, false
}

func vscodeExtensionServer(srv *ServerConfig) (string, []string, bool) {
	extDir := vscodeExtensionsDir()
	if extDir == "" {
		return "", nil, false
	}

	for _, lang := range srv.ExtensionToLanguage {
		switch lang {
		case "python":
			return vscodePyrightServer(extDir)
		case "rust":
			return vscodeRustAnalyzerServer(extDir)
		case "yaml":
			return vscodeYAMLServer(extDir)
		case "vue":
			return vscodeVolarServer(extDir)
		}
	}
	return "", nil, false
}

func knownInstallDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if home != "" {
		if isWindows() {
			dirs = append(dirs,
				filepath.Join(home, "go", "bin"),
				filepath.Join(home, ".cargo", "bin"),
				filepath.Join(home, "AppData", "Roaming", "npm"),
				filepath.Join(home, "AppData", "Local", "Programs", "LLVM", "bin"),
				clangdInstallDir(home),
				"C:\\Program Files\\LLVM\\bin",
			)
		} else {
			dirs = append(dirs,
				filepath.Join(home, "go", "bin"),
				filepath.Join(home, ".cargo", "bin"),
				filepath.Join(home, ".local", "bin"),
				"/usr/local/bin",
				"/usr/bin",
			)
		}
	}
	return dirs
}

// isRustupProxy checks whether path is a rust-analyzer binary managed by
// rustup whose underlying component is not installed. It runs --version
// and returns true if the binary fails (proxies report "Unknown binary"
// and exit non-zero).
func isRustupProxy(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base != "rust-analyzer.exe" && base != "rust-analyzer" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	cargoBin := filepath.Join(home, ".cargo", "bin")
	if !strings.HasPrefix(strings.ToLower(path), strings.ToLower(cargoBin)) {
		return false
	}
	// Run --version to verify it's a real binary, not a proxy shim. A proxy
	// fails within milliseconds; the generous timeout only guards against a
	// hung binary, so a slow cold start (AV scan of a fresh 20MB file) is
	// not misclassified as a proxy.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	return cmd.Run() != nil
}

func vscodeExtensionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".vscode", "extensions")
}

func vscodePyrightServer(extDir string) (string, []string, bool) {
	// Pylance bundles pyright; look for pyright-langserver or the bundle
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return "", nil, false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(strings.ToLower(entry.Name()), "ms-python.vscode-pylance-") {
			continue
		}
		// Try standalone pyright-langserver first
		distDir := filepath.Join(extDir, entry.Name(), "dist")
		candidate := filepath.Join(distDir, "node_modules", ".bin", "pyright-langserver")
		if isWindows() {
			candidate += ".cmd"
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, []string{"--stdio"}, true
		}
		// Fallback: run the bundle with Node
		bundle := filepath.Join(distDir, "pyright.bundle.js")
		if info, err := os.Stat(bundle); err == nil && !info.IsDir() {
			nodePath, _ := exec.LookPath("node")
			if nodePath != "" {
				return nodePath, []string{bundle, "--stdio"}, true
			}
		}
	}
	return "", nil, false
}

func vscodeRustAnalyzerServer(extDir string) (string, []string, bool) {
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return "", nil, false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(strings.ToLower(entry.Name()), "rust-lang.rust-analyzer-") {
			continue
		}
		exe := "rust-analyzer"
		if isWindows() {
			exe += ".exe"
		}
		candidate := filepath.Join(extDir, entry.Name(), "server", exe)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil, true
		}
	}
	return "", nil, false
}

func vscodeYAMLServer(extDir string) (string, []string, bool) {
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return "", nil, false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(strings.ToLower(entry.Name()), "redhat.vscode-yaml-") {
			continue
		}
		nodePath, _ := exec.LookPath("node")
		if nodePath == "" {
			break
		}
		// Try npm bin first
		binDir := filepath.Join(extDir, entry.Name(), "node_modules", ".bin")
		exe := "yaml-language-server"
		if isWindows() {
			exe += ".cmd"
		}
		candidate := filepath.Join(binDir, exe)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, []string{"--stdio"}, true
		}
		// Fallback: run via node
		candidate = filepath.Join(extDir, entry.Name(), "dist", "languageserver.js")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return nodePath, []string{candidate, "--stdio"}, true
		}
	}
	return "", nil, false
}

func vscodeVolarServer(extDir string) (string, []string, bool) {
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return "", nil, false
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(strings.ToLower(entry.Name()), "vue.volar-") {
			continue
		}
		nodePath, _ := exec.LookPath("node")
		if nodePath == "" {
			break
		}
		// Try the dist/language-server.js
		jsPath := filepath.Join(extDir, entry.Name(), "dist", "language-server.js")
		packageJSON := filepath.Join(extDir, entry.Name(), "package.json")
		if info, err := os.Stat(jsPath); err == nil && !info.IsDir() {
			// Read package.json to get the entry point
			if data, err := os.ReadFile(packageJSON); err == nil {
				var pkg struct {
					Main string `json:"main"`
				}
				if json.Unmarshal(data, &pkg) == nil && pkg.Main != "" {
					mainPath := filepath.Join(extDir, entry.Name(), pkg.Main)
					if info, err := os.Stat(mainPath); err == nil && !info.IsDir() {
						return nodePath, []string{mainPath, "--stdio"}, true
					}
				}
			}
			return nodePath, []string{jsPath, "--stdio"}, true
		}
		// Try bin
		exe := "vue-language-server"
		if isWindows() {
			exe += ".cmd"
		}
		candidate := filepath.Join(extDir, entry.Name(), "node_modules", ".bin", exe)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, []string{"--stdio"}, true
		}
	}
	return "", nil, false
}

// clangdInstallDir finds a versioned clangd installation under ~/clangd/.
func clangdInstallDir(home string) string {
	clangdDir := filepath.Join(home, "clangd")
	entries, err := os.ReadDir(clangdDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "clangd_") {
			return filepath.Join(clangdDir, e.Name(), "bin")
		}
	}
	return ""
}

func isWindows() bool {
	return os.PathSeparator == '\\'
}
