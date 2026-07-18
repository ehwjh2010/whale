package lsp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIsWindows(t *testing.T) {
	// isWindows checks os.PathSeparator, which is '\\' on Windows
	got := isWindows()
	if os.PathSeparator == '\\' && !got {
		t.Fatal("expected isWindows() = true on Windows")
	}
	if os.PathSeparator != '\\' && got {
		t.Fatal("expected isWindows() = false on non-Windows")
	}
}

func TestKnownInstallDirs_ReturnsDirectories(t *testing.T) {
	dirs := knownInstallDirs()
	if len(dirs) == 0 {
		t.Fatal("expected at least one known install directory")
	}
	for _, dir := range dirs {
		if dir == "" {
			t.Fatal("known install dirs should not contain empty strings")
		}
	}
	// Should include Go bin path
	hasGoBin := false
	for _, d := range dirs {
		if strings.Contains(d, "go") && strings.Contains(d, "bin") {
			hasGoBin = true
			break
		}
	}
	if !hasGoBin {
		t.Fatal("expected Go bin directory in known install dirs")
	}
}

func TestVSCodeExtensionsDir_ReturnsPath(t *testing.T) {
	dir := vscodeExtensionsDir()
	if dir == "" {
		t.Fatal("expected non-empty VS Code extensions dir")
	}
	if !strings.Contains(dir, ".vscode") && !strings.Contains(dir, "vscode") {
		t.Fatalf("expected VS Code path, got: %s", dir)
	}
}

func TestFindServerForConfig_PathLookup(t *testing.T) {
	// Find a binary that is guaranteed to be on PATH (like "go" or "git")
	// This tests the exec.LookPath path in FindServerForConfig
	srv := &ServerConfig{
		Command:             "go",
		ExtensionToLanguage: map[string]string{".go": "go"},
	}
	path, args, found := FindServerForConfig(srv)
	if !found {
		t.Fatal("expected to find 'go' on PATH")
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}
	if len(args) == 0 {
		// args should be a copy of srv.Args (empty)
	}
	_ = args
}

func TestFindServerForConfig_KnownInstallDir(t *testing.T) {
	dir := t.TempDir()
	// Create a dummy executable in a known install directory
	exeName := "my-fake-lsp"
	if isWindows() {
		exeName += ".exe"
	}
	exePath := filepath.Join(dir, exeName)
	// 0o755: exec.LookPath on Unix only matches files with the execute bit.
	if err := os.WriteFile(exePath, []byte("dummy"), 0o755); err != nil {
		t.Fatalf("create dummy exe: %v", err)
	}

	// Override knownInstallDirs by patching via a mock is not possible since it's in the same package
	// Instead, we'll verify that the function falls through to knownInstallDirs by using a
	// temporary PATH
	oldPath := os.Getenv("PATH")
	defer os.Setenv("PATH", oldPath)
	os.Setenv("PATH", dir)

	srv := &ServerConfig{
		Command:             "my-fake-lsp",
		ExtensionToLanguage: map[string]string{".xyz": "xyz"},
	}
	path, _, found := FindServerForConfig(srv)
	if !found {
		t.Fatal("expected to find my-fake-lsp on PATH")
	}
	if path != exePath {
		// On Windows, exec.LookPath may resolve to a different path format
		if !strings.EqualFold(path, exePath) && !strings.Contains(path, exeName) {
			t.Fatalf("expected path %q, got %q", exePath, path)
		}
	}
}

func TestFindServerForConfig_VSCodeFallbackDoesNotDuplicateArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	binDir := filepath.Join(home, ".vscode", "extensions", "ms-python.vscode-pylance-2024.1.0", "dist", "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	exe := "pyright-langserver"
	if isWindows() {
		exe += ".cmd"
	}
	if err := os.WriteFile(filepath.Join(binDir, exe), []byte("dummy"), 0o755); err != nil {
		t.Fatalf("write fake server: %v", err)
	}
	// Keep PATH from resolving a real binary.
	t.Setenv("PATH", t.TempDir())

	srv := &ServerConfig{
		Command:             "pyright-langserver",
		Args:                []string{"--stdio"},
		ExtensionToLanguage: map[string]string{".py": "python"},
	}
	path, args, found := FindServerForConfig(srv)
	if !found {
		t.Fatal("expected to find pylance-bundled server")
	}
	if !strings.Contains(path, "pylance") {
		t.Fatalf("expected vscode fallback path, got %q", path)
	}
	if want := []string{"--stdio"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v (fallback args must not be duplicated with configured args)", args, want)
	}
}

func TestFindServerForConfig_NotFound(t *testing.T) {
	srv := &ServerConfig{
		Command:             "this-definitely-does-not-exist-12345",
		ExtensionToLanguage: map[string]string{".xyz": "xyz"},
	}
	path, _, found := FindServerForConfig(srv)
	if found {
		t.Fatalf("expected not found, got path: %s", path)
	}
}

func TestFindServerForConfig_ArgsAreCopied(t *testing.T) {
	srv := &ServerConfig{
		Command:             "go",
		Args:                []string{"version"},
		ExtensionToLanguage: map[string]string{".go": "go"},
	}
	_, args, found := FindServerForConfig(srv)
	if !found {
		t.Fatal("expected to find go")
	}
	// Should retain original args
	if len(args) != 1 || args[0] != "version" {
		t.Fatalf("expected args [version], got %v", args)
	}
}

func TestVSCodeExtensionServer_NoExtensionDir(t *testing.T) {
	// If extensions dir doesn't exist, fallback should return not found
	home, _ := os.UserHomeDir()
	origVscodeDir := filepath.Join(home, ".vscode", "extensions")
	_ = origVscodeDir // to avoid unused
	// Can't really override vscodeExtensionsDir in this package easily,
	// but we can verify the function handles the "not found" case gracefully
	srv := &ServerConfig{
		Command:             "pyright",
		ExtensionToLanguage: map[string]string{".py": "python"},
	}
	// This should just not find anything via VS Code extension path
	// and that's fine
	path, _, found := FindServerForConfig(srv)
	if found && path == "" {
		t.Fatal("found but empty path")
	}
}

func TestVSCodeExtensionServer_UnknownLanguage(t *testing.T) {
	path, args, found := vscodeExtensionServer(&ServerConfig{
		ExtensionToLanguage: map[string]string{".xyz": "unknownlang"},
	})
	if found {
		t.Fatalf("expected not found for unknown language, got path=%q args=%v", path, args)
	}
}

func TestVSCodeExtensionServer_EmptyExtensionsDir(t *testing.T) {
	// Temporarily set HOME to a temp directory so vscodeExtensionsDir is empty
	oldHome := os.Getenv("HOME")
	if oldHome == "" {
		oldHome = os.Getenv("USERPROFILE")
	}
	defer func() {
		if oldHome != "" {
			if isWindows() {
				os.Setenv("USERPROFILE", oldHome)
			} else {
				os.Setenv("HOME", oldHome)
			}
		}
	}()

	tmpHome := t.TempDir()
	if isWindows() {
		os.Setenv("USERPROFILE", tmpHome)
	} else {
		os.Setenv("HOME", tmpHome)
	}

	srv := &ServerConfig{
		ExtensionToLanguage: map[string]string{".py": "python"},
	}
	path, args, found := vscodeExtensionServer(srv)
	if found {
		t.Fatalf("expected not found with empty extension dir, got path=%q", path)
	}
	_ = args
}

func TestKnownInstallDirs_Windows(t *testing.T) {
	if !isWindows() {
		t.Skip("Windows-only test")
	}
	dirs := knownInstallDirs()
	hasNpmDir := false
	for _, d := range dirs {
		if strings.Contains(d, "npm") || strings.Contains(d, "AppData") {
			hasNpmDir = true
			break
		}
	}
	if !hasNpmDir {
		t.Fatalf("expected npm/AppData dir on Windows, got: %v", dirs)
	}
}

func TestVSCodePyrightServer_NoPylance(t *testing.T) {
	dir := t.TempDir()
	// Create a fake .vscode/extensions dir but without Pylance
	extDir := filepath.Join(dir, ".vscode", "extensions")
	os.MkdirAll(extDir, 0755)

	// We can't easily mock vscodeExtensionsDir since it's in the same package,
	// but we can test the pyright-specific function with a non-existent dir
	path, args, found := vscodePyrightServer("nonexistent-dir")
	if found {
		t.Fatalf("expected not found, got path=%q", path)
	}
	_ = args
}

func TestVSCodeRustAnalyzerServer_NoExtension(t *testing.T) {
	path, args, found := vscodeRustAnalyzerServer("nonexistent-dir")
	if found {
		t.Fatalf("expected not found, got path=%q", path)
	}
	_ = args
}

func TestVSCodeYAMLServer_NoExtension(t *testing.T) {
	path, args, found := vscodeYAMLServer("nonexistent-dir")
	if found {
		t.Fatalf("expected not found, got path=%q", path)
	}
	_ = args
}

func TestVSCodeVolarServer_NoExtension(t *testing.T) {
	path, args, found := vscodeVolarServer("nonexistent-dir")
	if found {
		t.Fatalf("expected not found, got path=%q", path)
	}
	_ = args
}

func TestFindServerForConfig_NoArgsPreserved(t *testing.T) {
	srv := &ServerConfig{
		Command:             "go",
		ExtensionToLanguage: map[string]string{".go": "go"},
	}
	_, args, found := FindServerForConfig(srv)
	if !found {
		t.Fatal("expected to find go")
	}
	if len(args) != 0 {
		t.Fatalf("expected empty args, got %v", args)
	}
}

func TestIsRustupProxy_NonRustBinary(t *testing.T) {
	if isRustupProxy("gopls") {
		t.Fatal("should return false for non-rust binary")
	}
}

func TestIsRustupProxy_OutsideCargoBin(t *testing.T) {
	if isRustupProxy("C:\\tools\\rust-analyzer.exe") {
		t.Fatal("should return false when not in ~/.cargo/bin")
	}
}

// TestMain lets this test binary impersonate an executable under test:
// discovery and client code probe/spawn binaries, and a copied test binary
// behaves according to WHALE_LSP_FAKE_MODE before any tests run. This keeps
// process-related tests hermetic (no real servers, no timing dependence).
func TestMain(m *testing.M) {
	switch os.Getenv("WHALE_LSP_FAKE_MODE") {
	case "ok":
		os.Exit(0)
	case "fail":
		os.Exit(1)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeExecutable copies the test binary to dir/name (adding .exe on Windows)
// and selects its behavior via WHALE_LSP_FAKE_MODE (see TestMain).
func fakeExecutable(t *testing.T, dir, name, mode string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if isWindows() {
		name += ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("install fake executable: %v", err)
	}
	t.Setenv("WHALE_LSP_FAKE_MODE", mode)
	return dst
}

// fakeRustAnalyzer installs a fake rust-analyzer under a temp home's
// .cargo/bin and points HOME/USERPROFILE at that home.
func fakeRustAnalyzer(t *testing.T, mode string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return fakeExecutable(t, filepath.Join(home, ".cargo", "bin"), "rust-analyzer", mode)
}

// The rustup proxy shim lives in ~/.cargo/bin, which rustup puts on PATH —
// so the guard must also apply to the exec.LookPath hit, not only to the
// knownInstallDirs fallback.
func TestFindServerForConfig_SkipsRustupProxyOnPath(t *testing.T) {
	stub := fakeRustAnalyzer(t, "fail")
	t.Setenv("PATH", filepath.Dir(stub))
	_, _, found := FindServerForConfig(&ServerConfig{
		Command:             "rust-analyzer",
		ExtensionToLanguage: map[string]string{".rs": "rust"},
	})
	if found {
		t.Fatal("rustup proxy stub on PATH must be skipped, not returned as a working server")
	}
}

func TestIsRustupProxy_WorkingBinaryNotFlagged(t *testing.T) {
	path := fakeRustAnalyzer(t, "ok")
	if isRustupProxy(path) {
		t.Fatalf("working rust-analyzer at %s should not be flagged as proxy", path)
	}
}

func TestIsRustupProxy_StubFlagged(t *testing.T) {
	path := fakeRustAnalyzer(t, "fail")
	if !isRustupProxy(path) {
		t.Fatalf("failing rustup proxy stub at %s should be flagged", path)
	}
}

func TestClangdInstallDir_Empty(t *testing.T) {
	dir := t.TempDir()
	got := clangdInstallDir(dir)
	if got != "" {
		t.Fatalf("expected empty for empty dir, got %q", got)
	}
}

func TestClangdInstallDir_Found(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "clangd", "clangd_19.1.2", "bin"), 0755)
	got := clangdInstallDir(dir)
	if got == "" || !strings.HasSuffix(got, "bin") {
		t.Fatalf("expected bin path, got %q", got)
	}
}

func TestClangdInstallDir_NoMatch(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "clangd", "other_dir", "bin"), 0755)
	got := clangdInstallDir(dir)
	if got != "" {
		t.Fatalf("expected empty for non-clangd dir, got %q", got)
	}
}
