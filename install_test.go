package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- Manifest generation tests ---

func TestBuildManifestChrome(t *testing.T) {
	data, err := buildManifest("chrome", "/usr/local/bin/yomitan-host")
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if m["name"] != "yomitan_api" {
		t.Errorf("name = %v", m["name"])
	}
	if m["type"] != "stdio" {
		t.Errorf("type = %v", m["type"])
	}
	if m["path"] != "/usr/local/bin/yomitan-host" {
		t.Errorf("path = %v", m["path"])
	}

	origins, ok := m["allowed_origins"].([]interface{})
	if !ok || len(origins) == 0 {
		t.Fatalf("allowed_origins missing or empty: %v", m["allowed_origins"])
	}
	if origins[0] != "chrome-extension://likgccmbimhjbgkjambclfkhldnlhbnn/" {
		t.Errorf("origin = %v", origins[0])
	}

	// Firefox key should not be present
	if _, exists := m["allowed_extensions"]; exists {
		t.Error("Chrome manifest should not have allowed_extensions")
	}
}

func TestBuildManifestFirefox(t *testing.T) {
	data, err := buildManifest("firefox", "/usr/local/bin/yomitan-host")
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	extensions, ok := m["allowed_extensions"].([]interface{})
	if !ok || len(extensions) == 0 {
		t.Fatalf("allowed_extensions missing or empty: %v", m["allowed_extensions"])
	}
	if extensions[0] != "{6b733b82-9261-47ee-a595-2dda294a4d08}" {
		t.Errorf("firefox extension ID = %v", extensions[0])
	}

	// Chrome key should not be present
	if _, exists := m["allowed_origins"]; exists {
		t.Error("Firefox manifest should not have allowed_origins")
	}
}

func TestBuildManifestUnknownBrowser(t *testing.T) {
	_, err := buildManifest("netscape", "/tmp/host")
	if err == nil {
		t.Fatal("expected error for unknown browser")
	}
}

func TestBuildManifestAllBrowsers(t *testing.T) {
	for name := range browsers {
		data, err := buildManifest(name, "/tmp/host")
		if err != nil {
			t.Errorf("buildManifest(%q): %v", name, err)
			continue
		}

		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Errorf("buildManifest(%q): invalid JSON: %v", name, err)
			continue
		}

		if m["name"] != "yomitan_api" {
			t.Errorf("%s: name = %v", name, m["name"])
		}
		if m["type"] != "stdio" {
			t.Errorf("%s: type = %v", name, m["type"])
		}
		if m["path"] != "/tmp/host" {
			t.Errorf("%s: path = %v", name, m["path"])
		}
	}
}

// --- Install paths tests ---

func TestGetInstallPathsReturnsCurrentPlatform(t *testing.T) {
	paths, err := getInstallPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one install path")
	}

	// Every path should have a non-empty browser name and path
	for _, p := range paths {
		if p.browser == "" {
			t.Error("empty browser name")
		}
		if p.path == "" {
			t.Errorf("empty path for browser %q", p.browser)
		}
	}
}

func TestGetInstallPathsIncludesChrome(t *testing.T) {
	paths, err := getInstallPaths()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, p := range paths {
		if p.browser == "chrome" {
			found = true
			break
		}
	}
	if !found {
		t.Error("chrome not found in install paths")
	}
}

func TestGetInstallPathsIncludesFirefox(t *testing.T) {
	paths, err := getInstallPaths()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, p := range paths {
		if p.browser == "firefox" {
			found = true
			break
		}
	}
	if !found {
		t.Error("firefox not found in install paths")
	}
}

// --- Chrome fork configuration tests ---

func TestChromeForkBrowsersAreConfigured(t *testing.T) {
	for browser := range chromeForkBrowsers {
		if _, ok := browsers[browser]; !ok {
			t.Errorf("chromeForkBrowsers has %q but it's not in browsers config", browser)
		}
	}
}

func TestChromeForkBrowsersUseAllowedOrigins(t *testing.T) {
	// All Chrome forks should use allowed_origins (same as Chrome)
	for browser := range chromeForkBrowsers {
		cfg := browsers[browser]
		if cfg.extensionIDKey != "allowed_origins" {
			t.Errorf("%s uses %q, expected 'allowed_origins'", browser, cfg.extensionIDKey)
		}
	}
}

// --- installToDir tests (uses temp directories) ---

func TestInstallToDir(t *testing.T) {
	// Create a temp dir to act as the install target
	installDir := t.TempDir()

	// Create a fake executable to copy
	fakeExe := filepath.Join(t.TempDir(), "yomitan-host")
	if err := os.WriteFile(fakeExe, []byte("fake binary content"), 0755); err != nil {
		t.Fatal(err)
	}

	err := installToDir("chrome", installDir, fakeExe)
	if err != nil {
		t.Fatalf("installToDir: %v", err)
	}

	// Check manifest was written
	manifestPath := filepath.Join(installDir, "yomitan_api.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid manifest JSON: %v", err)
	}

	if m["name"] != "yomitan_api" {
		t.Errorf("manifest name = %v", m["name"])
	}
	if m["type"] != "stdio" {
		t.Errorf("manifest type = %v", m["type"])
	}

	// On non-Windows, binary should have been copied
	if runtime.GOOS != "windows" {
		copiedBin := filepath.Join(installDir, "yomitan-host")
		info, err := os.Stat(copiedBin)
		if err != nil {
			t.Fatalf("binary not copied: %v", err)
		}
		if info.Mode()&0111 == 0 {
			t.Error("copied binary is not executable")
		}

		// Manifest path should point to the copied location
		if m["path"] != copiedBin {
			t.Errorf("manifest path = %v, want %v", m["path"], copiedBin)
		}
	}
}

func TestInstallToDirMultipleBrowsers(t *testing.T) {
	installDir := t.TempDir()

	fakeExe := filepath.Join(t.TempDir(), "yomitan-host")
	if err := os.WriteFile(fakeExe, []byte("fake"), 0755); err != nil {
		t.Fatal(err)
	}

	// Install for multiple browsers to the same directory — should not conflict
	for _, browser := range []string{"chrome", "firefox", "brave"} {
		if err := installToDir(browser, installDir, fakeExe); err != nil {
			t.Errorf("installToDir(%q): %v", browser, err)
		}
	}

	// All should have written the same manifest file (last writer wins)
	manifestPath := filepath.Join(installDir, "yomitan_api.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("manifest missing: %v", err)
	}
}

func TestInstallToDirCreatesDirectories(t *testing.T) {
	base := t.TempDir()
	deepDir := filepath.Join(base, "a", "b", "c", "NativeMessagingHosts")

	fakeExe := filepath.Join(t.TempDir(), "yomitan-host")
	if err := os.WriteFile(fakeExe, []byte("fake"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := installToDir("chrome", deepDir, fakeExe); err != nil {
		t.Fatalf("installToDir with nested dirs: %v", err)
	}

	manifestPath := filepath.Join(deepDir, "yomitan_api.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("manifest not created in nested dir: %v", err)
	}
}

func TestInstallToDirManifestContent(t *testing.T) {
	installDir := t.TempDir()

	fakeExe := filepath.Join(t.TempDir(), "yomitan-host")
	if err := os.WriteFile(fakeExe, []byte("fake"), 0755); err != nil {
		t.Fatal(err)
	}

	// Test each browser produces correct manifest keys
	tests := []struct {
		browser    string
		wantKey    string
		wantAbsent string
	}{
		{"firefox", "allowed_extensions", "allowed_origins"},
		{"chrome", "allowed_origins", "allowed_extensions"},
		{"brave", "allowed_origins", "allowed_extensions"},
		{"arc", "allowed_origins", "allowed_extensions"},
	}

	for _, tt := range tests {
		dir := filepath.Join(installDir, tt.browser)
		if err := installToDir(tt.browser, dir, fakeExe); err != nil {
			t.Errorf("%s: %v", tt.browser, err)
			continue
		}

		data, _ := os.ReadFile(filepath.Join(dir, "yomitan_api.json"))
		var m map[string]interface{}
		json.Unmarshal(data, &m)

		if _, ok := m[tt.wantKey]; !ok {
			t.Errorf("%s: missing key %q", tt.browser, tt.wantKey)
		}
		if _, ok := m[tt.wantAbsent]; ok {
			t.Errorf("%s: unexpected key %q", tt.browser, tt.wantAbsent)
		}
	}
}

// --- copyFile tests ---

func TestCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "subdir", "dest")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestCopyFileSourceMissing(t *testing.T) {
	err := copyFile("/nonexistent/path", filepath.Join(t.TempDir(), "dst"))
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}
