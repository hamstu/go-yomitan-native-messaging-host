package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const manifestName = "yomitan_api"

type browserConfig struct {
	extensionIDKey string
	extensionIDs   []string
}

type installPath struct {
	browser string
	path    string
}

var browsers = map[string]browserConfig{
	"firefox": {
		extensionIDKey: "allowed_extensions",
		extensionIDs:   []string{"{6b733b82-9261-47ee-a595-2dda294a4d08}"},
	},
	"chrome": {
		extensionIDKey: "allowed_origins",
		extensionIDs:   []string{"chrome-extension://likgccmbimhjbgkjambclfkhldnlhbnn/"},
	},
	"chromium": {
		extensionIDKey: "allowed_origins",
		extensionIDs:   []string{"chrome-extension://likgccmbimhjbgkjambclfkhldnlhbnn/"},
	},
	"edge": {
		extensionIDKey: "allowed_origins",
		extensionIDs:   []string{"chrome-extension://likgccmbimhjbgkjambclfkhldnlhbnn/"},
	},
	"brave": {
		extensionIDKey: "allowed_origins",
		extensionIDs:   []string{"chrome-extension://likgccmbimhjbgkjambclfkhldnlhbnn/"},
	},
	"arc": {
		extensionIDKey: "allowed_origins",
		extensionIDs:   []string{"chrome-extension://likgccmbimhjbgkjambclfkhldnlhbnn/"},
	},
}

// Chromium forks (Brave, Arc, Edge) often ignore their own native messaging
// host directory and only check Chrome's. When installing for these browsers,
// we also install into Chrome's path automatically.
// See: https://github.com/yomidevs/yomitan-api/issues/11
var chromeForkBrowsers = map[string]bool{
	"brave": true,
	"arc":   true,
	"edge":  true,
}

func getInstallPaths() ([]installPath, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return []installPath{
			{"firefox", filepath.Join(home, "Library", "Application Support", "Mozilla", "NativeMessagingHosts")},
			{"chrome", filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "NativeMessagingHosts")},
			{"chromium", filepath.Join(home, "Library", "Application Support", "Chromium", "NativeMessagingHosts")},
			{"brave", filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "NativeMessagingHosts")},
			{"arc", filepath.Join(home, "Library", "Application Support", "Arc", "User Data", "NativeMessagingHosts")},
		}, nil
	case "linux":
		return []installPath{
			{"firefox", filepath.Join(home, ".mozilla", "native-messaging-hosts")},
			{"chrome", filepath.Join(home, ".config", "google-chrome", "NativeMessagingHosts")},
			{"chromium", filepath.Join(home, ".config", "chromium", "NativeMessagingHosts")},
			{"brave", filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser", "NativeMessagingHosts")},
		}, nil
	case "windows":
		// On Windows the manifest goes next to the executable; registry handles the rest.
		// For now we support file-based install only (registry requires elevated permissions).
		exePath, err := os.Executable()
		if err != nil {
			return nil, err
		}
		exeDir := filepath.Dir(exePath)
		return []installPath{
			{"firefox", exeDir},
			{"chrome", exeDir},
			{"chromium", exeDir},
			{"edge", exeDir},
			{"brave", exeDir},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func buildManifest(browser string, hostPath string) ([]byte, error) {
	cfg, ok := browsers[browser]
	if !ok {
		return nil, fmt.Errorf("unknown browser: %s", browser)
	}

	manifest := map[string]interface{}{
		"name":        manifestName,
		"description": "Yomitan API",
		"type":        "stdio",
		"path":        hostPath,
		cfg.extensionIDKey: cfg.extensionIDs,
	}

	return json.MarshalIndent(manifest, "", "    ")
}

func runInstall(args []string) {
	// Parse --browser flag
	var browserFlag string
	for i, arg := range args {
		if arg == "--browser" && i+1 < len(args) {
			browserFlag = strings.ToLower(args[i+1])
		} else if strings.HasPrefix(arg, "--browser=") {
			browserFlag = strings.ToLower(strings.TrimPrefix(arg, "--browser="))
		}
	}

	installPaths, err := getInstallPaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Build list of available browsers for this platform
	available := make(map[string]string) // browser -> install dir
	var availableNames []string
	for _, ip := range installPaths {
		available[ip.browser] = ip.path
		availableNames = append(availableNames, ip.browser)
	}

	// Validate --browser flag if provided
	if browserFlag != "" {
		if _, ok := available[browserFlag]; !ok {
			fmt.Fprintf(os.Stderr, "Error: browser %q is not available on %s\n", browserFlag, runtime.GOOS)
			fmt.Fprintf(os.Stderr, "Available browsers:\n")
			for _, name := range availableNames {
				fmt.Fprintf(os.Stderr, "  %s\n", name)
			}
			os.Exit(1)
		}
	}

	// Resolve the path to this executable
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not determine executable path: %v\n", err)
		os.Exit(1)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not resolve executable path: %v\n", err)
		os.Exit(1)
	}

	// Determine which browsers to install for
	var targets []string
	if browserFlag != "" {
		targets = []string{browserFlag}
	} else {
		targets = availableNames
		fmt.Printf("Installing native messaging host for all browsers on %s...\n\n", runtime.GOOS)
	}

	// Track which directories we've already installed to, so the Chrome-fork
	// fallback doesn't duplicate work.
	installed := make(map[string]bool)

	for _, browser := range targets {
		installDir := available[browser]

		if !installed[installDir] {
			fmt.Printf("[%s]\n", browser)
			if err := installToDir(browser, installDir, exePath); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: %v\n", err)
			}
			installed[installDir] = true
		} else {
			fmt.Printf("[%s] already covered by %s\n", browser, installDir)
		}

		// Chromium forks: also install to Chrome's path
		if chromeForkBrowsers[browser] {
			if chromeDir, ok := available["chrome"]; ok && chromeDir != installDir && !installed[chromeDir] {
				fmt.Printf("  Note: %s is a Chromium fork — also installing to Chrome's path...\n", browser)
				if err := installToDir(browser, chromeDir, exePath); err != nil {
					fmt.Fprintf(os.Stderr, "  Warning: could not install to Chrome path: %v\n", err)
				}
				installed[chromeDir] = true
			}
		}
	}

	fmt.Printf("\nDone.\n")
}

func installToDir(browser, installDir, exePath string) error {
	// On macOS/Linux, copy the binary into the native messaging hosts directory
	// so it lives where the browser expects it.
	hostPath := exePath
	if runtime.GOOS != "windows" {
		destPath := filepath.Join(installDir, filepath.Base(exePath))
		if destPath != exePath {
			if err := copyFile(exePath, destPath); err != nil {
				return fmt.Errorf("copying binary: %w", err)
			}
			if err := os.Chmod(destPath, 0755); err != nil {
				return fmt.Errorf("setting permissions: %w", err)
			}
			hostPath = destPath
			fmt.Printf("Binary copied to %s\n", destPath)
		}
	}

	// Generate and write the manifest
	manifestBytes, err := buildManifest(browser, hostPath)
	if err != nil {
		return fmt.Errorf("building manifest: %w", err)
	}

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", installDir, err)
	}

	manifestPath := filepath.Join(installDir, manifestName+".json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	fmt.Printf("Manifest installed to %s\n", manifestPath)
	return nil
}

func copyFile(src, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}
