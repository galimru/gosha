package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Secret keys Gosha reads. Config files never hold secrets.
const (
	KeyLinearClientID     = "LINEAR_CLIENT_ID"
	KeyLinearClientSecret = "LINEAR_CLIENT_SECRET"
	KeyNotionToken        = "NOTION_TOKEN"
	KeyClaudeToken        = "CLAUDE_CODE_OAUTH_TOKEN"
	KeyTelegramToken      = "TELEGRAM_BOT_TOKEN"
)

// Secrets resolves secret keys: the project's secrets.env
// first, then the shared secrets.env, then the environment.
type Secrets struct {
	layers []map[string]string
	env    func(string) (string, bool)
	red    *Redactor
}

// LoadSecrets reads the shared secrets file and, if project is not empty,
// that project's overrides. Missing files are fine; files that group or
// others can read are refused.
func LoadSecrets(h Home, project string, red *Redactor) (*Secrets, error) {
	s := &Secrets{env: os.LookupEnv, red: red}
	if project != "" {
		m, err := ReadEnvFile(h.Project(project).Secrets())
		if err != nil {
			return nil, err
		}
		s.layers = append(s.layers, m)
	}
	m, err := ReadEnvFile(h.Secrets())
	if err != nil {
		return nil, err
	}
	s.layers = append(s.layers, m)
	return s, nil
}

// NewSecrets builds a resolver from explicit layers, for tests.
func NewSecrets(env func(string) (string, bool), red *Redactor, layers ...map[string]string) *Secrets {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	return &Secrets{layers: layers, env: env, red: red}
}

// Lookup returns the first non-empty value for key. Every value it hands
// out is registered with the redactor.
func (s *Secrets) Lookup(key string) (string, bool) {
	for _, l := range s.layers {
		if v, ok := l[key]; ok && v != "" {
			s.red.Add(v)
			return v, true
		}
	}
	if v, ok := s.env(key); ok && v != "" {
		s.red.Add(v)
		return v, true
	}
	return "", false
}

// Require returns key's value, or an error saying where to set it.
func (s *Secrets) Require(key string) (string, error) {
	if v, ok := s.Lookup(key); ok {
		return v, nil
	}
	return "", fmt.Errorf("%s is not set: add it to secrets.env", key)
}

// Credentials are the secrets a project's runs need.
type Credentials struct {
	LinearClientID     string
	LinearClientSecret string
	NotionToken        string
	ClaudeToken        string
}

// ProjectCredentials resolves every secret a project needs, failing on
// the first missing one.
func (s *Secrets) ProjectCredentials() (Credentials, error) {
	var c Credentials
	for _, f := range []struct {
		key string
		dst *string
	}{
		{KeyLinearClientID, &c.LinearClientID},
		{KeyLinearClientSecret, &c.LinearClientSecret},
		{KeyNotionToken, &c.NotionToken},
		{KeyClaudeToken, &c.ClaudeToken},
	} {
		v, err := s.Require(f.key)
		if err != nil {
			return Credentials{}, err
		}
		*f.dst = v
	}
	return c, nil
}

// ReadEnvFile parses KEY=value lines. Blank lines and lines starting with
// '#' are ignored, and one pair of surrounding quotes is stripped.
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := CheckPrivate(f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		if !ok || !envKey.MatchString(k) {
			return nil, fmt.Errorf("%s:%d: expected KEY=value", path, n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		m[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// CheckPrivate fails if group or others have any access to the file.
func CheckPrivate(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("permissions %04o are too open, run: chmod 600 %s", perm, f.Name())
	}
	return nil
}
