// Copyright 2019 Google Inc. All rights reserved.
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
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
)

func TestSetRemote(t *testing.T) {
	dir := t.TempDir()

	script := `mkdir orig
cd orig
git init -b master
cd ..
git clone orig/.git clone.git
`

	cmd := exec.Command("/bin/sh", "-euxc", script)
	cmd.Dir = dir

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execution error: %v, output %s", err, out)
	}

	r := dir + "/clone.git"
	if err := setFetch(r, "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		t.Fatalf("addFetch: %v", err)
	}

	repo, err := git.PlainOpen(r)
	if err != nil {
		t.Fatal("PlainOpen", err)
	}

	rm, err := repo.Remote("origin")
	if err != nil {
		t.Fatal("Remote", err)
	}
	if got, want := rm.Config().Fetch[0].String(), "+refs/heads/*:refs/heads/*"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func gitConfigValue(t *testing.T, repoDir, key string) (string, bool) {
	t.Helper()

	cmd := exec.Command("git", "-C", repoDir, "config", "--get", key)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	err := cmd.Run()
	if err == nil {
		return strings.TrimSuffix(stdout.String(), "\n"), true
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false
	}

	t.Fatalf("git config --get %s: %v", key, err)
	return "", false
}

func TestCloneRepoReturnsDestinationWhenSettingsChange(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runScript(t, root, "git init --bare "+origin)

	destRoot := filepath.Join(root, "repos")
	dest, err := CloneRepo(destRoot, "owner/repo", origin, map[string]string{
		"zoekt.name":        "github.com/owner/repo",
		"zoekt.description": "initial",
	})
	if err != nil {
		t.Fatalf("CloneRepo initial clone: %v", err)
	}

	repoDest := filepath.Join(destRoot, "owner", "repo.git")
	if dest != repoDest {
		t.Fatalf("got %q want %q", dest, repoDest)
	}

	dest, err = CloneRepo(destRoot, "owner/repo", origin, map[string]string{
		"zoekt.name":        "github.com/owner/repo",
		"zoekt.description": "initial",
	})
	if err != nil {
		t.Fatalf("CloneRepo no-op update: %v", err)
	}
	if dest != "" {
		t.Fatalf("got %q want empty destination for unchanged settings", dest)
	}

	dest, err = CloneRepo(destRoot, "owner/repo", origin, map[string]string{
		"zoekt.name":        "github.com/owner/repo",
		"zoekt.description": "updated",
	})
	if err != nil {
		t.Fatalf("CloneRepo changed settings: %v", err)
	}
	if dest != repoDest {
		t.Fatalf("got %q want %q when settings changed", dest, repoDest)
	}

	if got, ok := gitConfigValue(t, repoDest, "zoekt.description"); !ok || got != "updated" {
		t.Fatalf("got zoekt.description=%q exists=%t want updated/true", got, ok)
	}
}

func TestCloneRepoRemovesEmptyZoektSettingsAndUpdatesOriginURL(t *testing.T) {
	root := t.TempDir()
	originA := filepath.Join(root, "origin-a.git")
	originB := filepath.Join(root, "origin-b.git")
	runScript(t, root, "git init --bare "+originA)
	runScript(t, root, "git init --bare "+originB)

	destRoot := filepath.Join(root, "repos")
	if _, err := CloneRepo(destRoot, "owner/repo", originA, map[string]string{
		"zoekt.name":        "github.com/owner/repo",
		"zoekt.description": "present",
	}); err != nil {
		t.Fatalf("CloneRepo initial clone: %v", err)
	}

	dest, err := CloneRepo(destRoot, "owner/repo", originB, map[string]string{
		"zoekt.name":        "github.com/owner/repo",
		"zoekt.description": "",
	})
	if err != nil {
		t.Fatalf("CloneRepo update: %v", err)
	}

	repoDest := filepath.Join(destRoot, "owner", "repo.git")
	if dest != repoDest {
		t.Fatalf("got %q want %q", dest, repoDest)
	}

	if got, ok := gitConfigValue(t, repoDest, "zoekt.description"); ok {
		t.Fatalf("got stale zoekt.description=%q, want it removed", got)
	}
	if got, ok := gitConfigValue(t, repoDest, "remote.origin.url"); !ok || got != originB {
		t.Fatalf("got remote.origin.url=%q exists=%t want %q/true", got, ok, originB)
	}
}
