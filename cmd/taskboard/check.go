package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/kilo666mj/taskboard/internal/config"
	"github.com/kilo666mj/taskboard/internal/server"
	"github.com/kilo666mj/taskboard/internal/service"
)

// versionString reports the release version and, for source builds, the VCS
// revision embedded by the Go toolchain.
func versionString() string {
	version := "taskboard " + server.Version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision != "" {
		version += " (" + revision
		if modified == "true" {
			version += ", modified"
		}
		version += ")"
	}
	return version + " " + info.GoVersion
}

// checkConfig validates configuration without opening the database or
// listening, so a deployment can reject a bad environment before restarting
// the running service. envFile, when set, is a systemd EnvironmentFile whose
// variables replace the current environment's.
func checkConfig(envFile string) error {
	if envFile != "" {
		file, err := os.Open(envFile)
		if err != nil {
			return err
		}
		values, err := parseEnvironmentFile(file)
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", envFile, err)
		}
		if closeErr != nil {
			return closeErr
		}
		for key, value := range values {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := service.ValidateRequirements(cfg.DefaultRequirements); err != nil {
		return fmt.Errorf("TASKBOARD_DEFAULT_REQUIREMENTS: %w", err)
	}
	return nil
}

// parseEnvironmentFile reads KEY=VALUE lines as systemd does for the values
// the deployment template writes: blank lines and comments are skipped, and a
// double-quoted value is unescaped.
func parseEnvironmentFile(reader io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d is not KEY=VALUE", line)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			var unquoted string
			if err := json.Unmarshal([]byte(value), &unquoted); err != nil {
				return nil, fmt.Errorf("line %d: invalid quoted value for %s", line, key)
			}
			value = unquoted
		} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	return values, scanner.Err()
}
