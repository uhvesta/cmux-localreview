package githubauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileConfigStore persists only public GitHub App client IDs. It must never
// gain token fields: credentials belong exclusively in SecretStore.
type FileConfigStore struct {
	Path string
	mu   sync.Mutex
}

type fileConfig struct {
	ClientIDs map[string]string `json:"clientIds"`
}

func NewFileConfigStore(path string) *FileConfigStore { return &FileConfigStore{Path: path} }

func (s *FileConfigStore) readLocked() (fileConfig, error) {
	if strings.TrimSpace(s.Path) == "" {
		return fileConfig{}, fmt.Errorf("GitHub App configuration path is required")
	}
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return fileConfig{ClientIDs: map[string]string{}}, nil
	}
	if err != nil {
		return fileConfig{}, fmt.Errorf("read GitHub App configuration: %w", err)
	}
	var config fileConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fileConfig{}, fmt.Errorf("parse GitHub App configuration: %w", err)
	}
	if config.ClientIDs == nil {
		config.ClientIDs = map[string]string{}
	}
	return config, nil
}

func (s *FileConfigStore) ClientID(capability Capability) (string, error) {
	if !valid(capability) {
		return "", fmt.Errorf("unknown GitHub capability")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.readLocked()
	if err != nil {
		return "", err
	}
	return config.ClientIDs[string(capability)], nil
}

func (s *FileConfigStore) SetClientID(capability Capability, id string) error {
	if !valid(capability) {
		return fmt.Errorf("unknown GitHub capability")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.readLocked()
	if err != nil {
		return err
	}
	config.ClientIDs[string(capability)] = strings.TrimSpace(id)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create GitHub App configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.Path), ".github-app-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write GitHub App configuration: %w", err)
	}
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace GitHub App configuration: %w", err)
	}
	return nil
}
