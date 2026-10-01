// Package devboxworker runs leased coding jobs on explicitly trusted development hosts.
package devboxworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	Version         = "0.3.0"
	maxOutputBytes  = 1024 * 1024
	maxPatchBytes   = 1024 * 1024
	maxSummaryBytes = 4000
)

type Repository struct {
	Path      string `json:"path"`
	URL       string `json:"url"`
	OriginURL string `json:"origin_url,omitempty"`
}

type Config struct {
	ServerURL     string                `json:"server_url"`
	TokenFile     string                `json:"token_file"`
	WorkRoot      string                `json:"work_root"`
	ExecutionMode string                `json:"execution_mode"`
	LocalhostHTTP bool                  `json:"localhost_http,omitzero"`
	Repositories  map[string]Repository `json:"repositories"`
	Providers     map[string]string     `json:"providers"`
	PollInterval  time.Duration         `json:"-"`
}

func LoadConfig(path string) (Config, error) {
	var config Config
	file, err := os.Open(path)
	if err != nil {
		return config, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return config, errors.New("configuration must contain exactly one JSON object")
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	server, err := url.Parse(c.ServerURL)
	if err != nil || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return errors.New("server_url must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if server.Scheme != "https" {
		ip := net.ParseIP(server.Hostname())
		if server.Scheme != "http" || !c.LocalhostHTTP || (server.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("server_url requires HTTPS; localhost_http only permits explicit loopback HTTP")
		}
	}
	if c.ExecutionMode != "trusted-local" {
		return errors.New("execution_mode must explicitly be trusted-local: jobs and tests execute as this local user")
	}
	for label, path := range map[string]string{"token_file": c.TokenFile, "work_root": c.WorkRoot} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", label)
		}
	}
	if len(c.Repositories) == 0 || len(c.Providers) == 0 {
		return errors.New("configure at least one repository and provider")
	}
	for name, repository := range c.Repositories {
		if name == "" || !filepath.IsAbs(repository.Path) || repository.URL == "" {
			return fmt.Errorf("repository %q requires an absolute path and exact URL", name)
		}
		parsed, err := url.Parse(repository.URL)
		if err != nil || parsed.User != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("repository %q URL must be HTTPS without embedded credentials", name)
		}
		if err := validateOrigin(repository.OriginURL); err != nil {
			return fmt.Errorf("repository %q origin_url: %w", name, err)
		}
	}
	for id, path := range c.Providers {
		if id != "claude-code" && id != "codex" && id != "opencode" {
			return fmt.Errorf("unsupported provider %q", id)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("provider %q executable must be an absolute path", id)
		}
	}
	return nil
}

func validateOrigin(value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\x00\r\n\t ") {
		return errors.New("contains invalid characters")
	}
	if strings.HasPrefix(value, "git@") {
		host, path, ok := strings.Cut(strings.TrimPrefix(value, "git@"), ":")
		if ok && host != "" && path != "" && !strings.ContainsAny(host, "/@") {
			return nil
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be an HTTPS or SSH URL without credentials")
	}
	if parsed.Scheme == "https" && parsed.User == nil {
		return nil
	}
	if parsed.Scheme == "ssh" && parsed.User != nil && parsed.User.Username() == "git" {
		if _, hasPassword := parsed.User.Password(); !hasPassword {
			return nil
		}
	}
	return errors.New("must be an HTTPS or SSH URL without credentials")
}

// ReadToken never follows a final symlink or accepts a token readable by another user.
func ReadToken(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("open worker token: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect worker token: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || (info.Mode().Perm() != 0600 && info.Mode().Perm() != 0400) || int(stat.Uid) != os.Geteuid() || info.Size() > 256 {
		return "", errors.New("worker token must be a regular file owned by this user with mode 0600 or 0400")
	}
	data := make([]byte, 257)
	n, err := file.Read(data)
	if err != nil {
		return "", fmt.Errorf("read worker token: %w", err)
	}
	token := strings.TrimSpace(string(data[:n]))
	if !strings.HasPrefix(token, "kub_") || len(token) != 68 {
		return "", errors.New("invalid worker token format")
	}
	for _, character := range token[4:] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return "", errors.New("invalid worker token format")
		}
	}
	return token, nil
}
