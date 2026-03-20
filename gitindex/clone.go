// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitindex

import (
	"bytes"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	formatconfig "github.com/go-git/go-git/v5/plumbing/format/config"
)

type gitConfigPath struct {
	section    string
	subsection string
	key        string
}

func parseGitConfigPath(key string) (gitConfigPath, error) {
	parts := strings.SplitN(key, ".", 3)
	switch len(parts) {
	case 2:
		return gitConfigPath{section: parts[0], key: parts[1]}, nil
	case 3:
		return gitConfigPath{section: parts[0], subsection: parts[1], key: parts[2]}, nil
	default:
		return gitConfigPath{}, fmt.Errorf("invalid git config key %q", key)
	}
}

func lookupRawConfigOption(cfg *formatconfig.Config, path gitConfigPath) (string, bool) {
	if cfg == nil {
		return "", false
	}

	for _, section := range cfg.Sections {
		if !section.IsName(path.section) {
			continue
		}

		if path.subsection == "" {
			if !section.HasOption(path.key) {
				return "", false
			}
			return section.Option(path.key), true
		}

		for _, subsection := range section.Subsections {
			if !subsection.IsName(path.subsection) {
				continue
			}
			if !subsection.HasOption(path.key) {
				return "", false
			}
			return subsection.Option(path.key), true
		}

		return "", false
	}

	return "", false
}

func updateRawConfigOption(cfg *formatconfig.Config, path gitConfigPath, value string) bool {
	current, ok := lookupRawConfigOption(cfg, path)
	if value == "" {
		if !ok {
			return false
		}

		if path.subsection == "" {
			cfg.Section(path.section).RemoveOption(path.key)
		} else {
			cfg.Section(path.section).Subsection(path.subsection).RemoveOption(path.key)
		}

		return true
	}

	if ok && current == value {
		return false
	}

	if path.subsection == "" {
		cfg.Section(path.section).SetOption(path.key, value)
	} else {
		cfg.Section(path.section).Subsection(path.subsection).SetOption(path.key, value)
	}

	return true
}

func updateRemoteURL(cfg *config.Config, path gitConfigPath, value string) (bool, error) {
	if value == "" {
		return false, fmt.Errorf("remote URL for %q cannot be empty", path.subsection)
	}

	remote := cfg.Remotes[path.subsection]
	if remote == nil {
		remote = &config.RemoteConfig{Name: path.subsection}
		cfg.Remotes[path.subsection] = remote
	}

	current := ""
	if len(remote.URLs) > 0 {
		current = remote.URLs[0]
	}
	if current == value && len(remote.URLs) == 1 {
		return false, nil
	}

	remote.URLs = []string{value}
	return true, nil
}

func updateGitConfigOption(cfg *config.Config, key, value string) (bool, error) {
	path, err := parseGitConfigPath(key)
	if err != nil {
		return false, err
	}

	if path.section == "remote" && path.subsection != "" && path.key == "url" {
		return updateRemoteURL(cfg, path, value)
	}

	return updateRawConfigOption(cfg.Raw, path, value), nil
}

// Updates the zoekt.* git config options after a repo is cloned.
// Once a repo is cloned, we can no longer use the --config flag to update all
// of it's zoekt.* settings at once. `git config` is limited to one option at once.
func updateZoektGitConfig(repoDest string, settings map[string]string) (bool, error) {
	repo, err := git.PlainOpen(repoDest)
	if err != nil {
		return false, err
	}

	cfg, err := repo.Config()
	if err != nil {
		return false, err
	}

	var keys []string
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var changed bool
	for _, k := range keys {
		updated, err := updateGitConfigOption(cfg, k, settings[k])
		if err != nil {
			return false, err
		}
		changed = changed || updated
	}

	if !changed {
		return false, nil
	}

	if err := repo.Storer.SetConfig(cfg); err != nil {
		return false, err
	}

	return true, nil
}

// CloneRepo clones one repository, adding the given config
// settings. It returns the bare repo directory. The `name` argument
// determines where the repo is stored relative to `destDir`. Returns
// the directory of the repository.
func CloneRepo(destDir, name, cloneURL string, settings map[string]string) (string, error) {
	parent := filepath.Join(destDir, filepath.Dir(name))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}

	repoDest := filepath.Join(parent, filepath.Base(name)+".git")
	if _, err := os.Lstat(repoDest); err == nil {
		// Repository exists, ensure settings are in sync including the clone URL
		settings := maps.Clone(settings)
		settings["remote.origin.url"] = cloneURL
		hadUpdate, err := updateZoektGitConfig(repoDest, settings)
		if err != nil {
			return "", fmt.Errorf("failed to update repository settings: %w", err)
		}
		if hadUpdate {
			return repoDest, nil
		}
		return "", nil
	}

	var keys []string
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var config []string
	for _, k := range keys {
		if settings[k] != "" {
			config = append(config, "--config", k+"="+settings[k])
		}
	}

	cmd := exec.Command(
		"git", "clone", "--bare", "--verbose", "--progress",
	)
	cmd.Args = append(cmd.Args, config...)
	cmd.Args = append(cmd.Args, cloneURL, repoDest)

	// Prevent prompting
	cmd.Stdin = &bytes.Buffer{}
	log.Println("running:", cmd.Args)
	if err := cmd.Run(); err != nil {
		return "", err
	}

	if err := setFetch(repoDest, "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		log.Printf("addFetch: %v", err)
	}
	return repoDest, nil
}

func setFetch(repoDir, remote, refspec string) error {
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return err
	}

	cfg, err := repo.Config()
	if err != nil {
		return err
	}

	rm := cfg.Remotes[remote]
	if rm != nil {
		rm.Fetch = []config.RefSpec{config.RefSpec(refspec)}
	}
	if err := repo.Storer.SetConfig(cfg); err != nil {
		return err
	}

	return nil
}
