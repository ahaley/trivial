package llm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// gcloudProjectEnvVar is gcloud's own override for the configured project. It
// beats the config file, so Trivial has to honour it to agree with gcloud.
const gcloudProjectEnvVar = "CLOUDSDK_CORE_PROJECT"

// gcloudConfig is the part of gcloud's active configuration Trivial reads.
type gcloudConfig struct {
	Project string
	Account string
}

// readGcloudConfig returns the active gcloud configuration, or a zero value
// when there is none to read.
//
// The file is parsed directly rather than shelling out to `gcloud`: the CLI is
// a Python wrapper that costs seconds per invocation, and its configuration is
// still there to be read on machines where gcloud is not on PATH. Nothing here
// is an error — a machine without gcloud simply has no answer to give.
func readGcloudConfig() gcloudConfig {
	dir := gcloudConfigDir()
	if dir == "" {
		return gcloudConfig{}
	}
	b, err := os.ReadFile(filepath.Join(dir, "configurations", "config_"+activeGcloudConfigName(dir)))
	if err != nil {
		return gcloudConfig{}
	}

	var cfg gcloudConfig
	var section string
	for raw := range strings.Lines(string(b)) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if section != "core" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "project":
			cfg.Project = strings.TrimSpace(value)
		case "account":
			cfg.Account = strings.TrimSpace(value)
		}
	}
	return cfg
}

// gcloudConfigDir locates gcloud's configuration directory the way gcloud does.
func gcloudConfigDir() string {
	if dir := os.Getenv("CLOUDSDK_CONFIG"); dir != "" {
		return dir
	}
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "gcloud")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gcloud")
}

// activeGcloudConfigName reports which named configuration is in effect.
func activeGcloudConfigName(dir string) string {
	if name := os.Getenv("CLOUDSDK_ACTIVE_CONFIG_NAME"); name != "" {
		return name
	}
	if b, err := os.ReadFile(filepath.Join(dir, "active_config")); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name
		}
	}
	return "default"
}
