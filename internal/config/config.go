// Package config loads and saves OpenListFS settings. Credentials come from
// the environment or a .env file in the working directory; Save writes .env
// (git-ignored) so the GUI can persist what the user types.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config carries credentials. Never log it.
type Config struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
	IPFamily string `json:"ipFamily"`
	Proxy    string `json:"proxy"` // "", "none", "system" or a proxy URL
}

// Load reads OPENLISTFS_* from the environment; a .env file in the working
// directory provides defaults for unset variables.
func Load() (Config, error) {
	LoadDotEnv(".env")
	var cfg Config
	var missing []string
	cfg.URL = os.Getenv("OPENLISTFS_URL")
	cfg.Token = os.Getenv("OPENLISTFS_TOKEN")
	cfg.Username = os.Getenv("OPENLISTFS_USERNAME")
	cfg.Password = os.Getenv("OPENLISTFS_PASSWORD")
	cfg.Proxy = os.Getenv("OPENLISTFS_PROXY")
	cfg.IPFamily = os.Getenv("OPENLISTFS_IP_FAMILY")
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

// Save writes the four settings to .env, creating it if needed.
func Save(cfg Config) error {
	f, err := os.OpenFile(".env", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "OPENLISTFS_URL=%s\n", cfg.URL)
	fmt.Fprintf(f, "OPENLISTFS_TOKEN=%s\n", cfg.Token)
	fmt.Fprintf(f, "OPENLISTFS_USERNAME=%s\n", cfg.Username)
	fmt.Fprintf(f, "OPENLISTFS_PASSWORD=%s\n", cfg.Password)
	fmt.Fprintf(f, "OPENLISTFS_PROXY=%s\n", cfg.Proxy)
	fmt.Fprintf(f, "OPENLISTFS_IP_FAMILY=%s\n", cfg.IPFamily)
	return nil
}

// LoadDotEnv applies KEY=VALUE lines from path for variables not already set.
func LoadDotEnv(path string) {
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
