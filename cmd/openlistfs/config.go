package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// config carries the credentials for one run. It is never logged.
type config struct {
	URL      string
	Token    string
	Username string
	Password string
}

// loadConfig reads OPENLISTFS_* from the environment; a .env file in the
// working directory (KEY=VALUE lines) provides defaults for unset variables.
func loadConfig() (config, error) {
	loadDotEnv(".env")
	var cfg config
	var missing []string
	cfg.URL = os.Getenv("OPENLISTFS_URL")
	cfg.Token = os.Getenv("OPENLISTFS_TOKEN")
	cfg.Username = os.Getenv("OPENLISTFS_USERNAME")
	cfg.Password = os.Getenv("OPENLISTFS_PASSWORD")
	if cfg.URL == "" {
		missing = append(missing, "OPENLISTFS_URL")
	}
	if cfg.Token == "" && cfg.Username == "" {
		missing = append(missing, "OPENLISTFS_TOKEN (or OPENLISTFS_USERNAME + OPENLISTFS_PASSWORD)")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing configuration: %s (see .env.example)", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists && key != "" {
			os.Setenv(key, value)
		}
	}
}
