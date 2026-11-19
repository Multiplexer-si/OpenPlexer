package release_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestWorkflowValidatesTagBeforeBuild(t *testing.T) {
	bash := requireCommand(t, "bash")
	build, _, _ := strings.Cut(workflowJob(t, "build"), "\n  release:\n")
	script := workflowRunBlocks(t, build)
	path := recordingPath(t, "make", "go")
	for _, tag := range []string{version, "vnext", "v1", "v1.2.3+meta", "v1.2.3\n"} {
		t.Run(fmt.Sprintf("%q", tag), func(t *testing.T) {
			call := filepath.Join(t.TempDir(), "build")
			cmd := exec.CommandContext(t.Context(), bash, "-eo", "pipefail", "-c", script)
			cmd.Env = append(cmd.Environ(), "PATH="+path,
				"GITHUB_REF_TYPE=tag", "GITHUB_REF_NAME="+tag, "RELEASE_CALL="+call, "GITHUB_OUTPUT="+call+".output")
			output, err := cmd.CombinedOutput()
			if tag == version {
				if err != nil {
					t.Fatalf("valid tag failed: %v\n%s", err, output)
				}
				want := "go\nrun\n./tools/release\n-version\n" + tag + "\n"
				if got := string(read(t, call)); got != want {
					t.Errorf("build command = %q, want %q", got, want)
				}
				return
			}
			if err == nil || !strings.Contains(string(output), "invalid release tag") {
				t.Errorf("invalid tag reached the build step: %v\n%s", err, output)
			}
			if _, err := os.Stat(call); !os.IsNotExist(err) {
				t.Errorf("build command was called for invalid tag: %v", err)
			}
		})
	}
}

func TestMakeReleasePassesRawVersion(t *testing.T) {
	make := requireCommand(t, "make")
	requireCommand(t, "sh")
	tag := "v1.2.3$$\ninvalid'quote"
	call := filepath.Join(t.TempDir(), "build")
	cmd := exec.CommandContext(t.Context(), make, "release", "VERSION="+tag)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "PATH="+recordingPath(t, "go"), "RELEASE_CALL="+call)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make release: %v\n%s", err, output)
	}
	want := "go\nrun\n./tools/release\n-version\n" + tag + "\n"
	if got := string(read(t, call)); got != want {
		t.Errorf("make passed %q, want %q", got, want)
	}
}

func TestWorkflowPublishesExactlyVerifiedArchives(t *testing.T) {
	bash := requireCommand(t, "bash")
	requireCommand(t, "sha256sum")
	script := `gh() { printf '%s\n' "$@" > "$RELEASE_CALL"; }` + "\n" +
		workflowRunBlocks(t, workflowJob(t, "release"))
	archives := []string{
		"openplexer_" + version + "_darwin_amd64.tar.gz",
		"openplexer_" + version + "_darwin_arm64.tar.gz",
		"openplexer_" + version + "_linux_amd64.tar.gz",
		"openplexer_" + version + "_linux_arm64.tar.gz",
		"openplexer_" + version + "_windows_amd64.zip",
	}
	for _, scenario := range []string{"complete", "tag notes", "unlisted archive", "missing archive and entry", "unexpected file", "duplicate entry", "corrupted archive"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := t.TempDir()
			dir := filepath.Join(workspace, "dist", "release")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			notesFile := filepath.Join(workspace, ".github", "release-notes", version+".md")
			if scenario == "tag notes" {
				if err := os.MkdirAll(filepath.Dir(notesFile), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(notesFile, []byte("Notes for "+version+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			call := filepath.Join(t.TempDir(), "publish")
			var manifest strings.Builder
			for i, name := range archives {
				if i == 0 && scenario == "missing archive and entry" {
					continue
				}
				data := []byte(name)
				if i != 0 || scenario != "unlisted archive" {
					fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(data), name)
				}
				if i == 0 && scenario == "duplicate entry" {
					fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(data), name)
				}
				if i == 0 && scenario == "corrupted archive" {
					data = append(data, '!')
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unexpected file" {
				if err := os.WriteFile(filepath.Join(dir, ".extra"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(manifest.String()), 0644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), bash, "-eo", "pipefail", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(cmd.Environ(), "GITHUB_REF_NAME="+version, "GITHUB_REPOSITORY=example/project",
				"GITHUB_WORKSPACE="+workspace, "RELEASE_CALL="+call)
			output, err := cmd.CombinedOutput()
			if scenario != "complete" && scenario != "tag notes" {
				if err == nil {
					t.Errorf("publish succeeded with %s:\n%s", scenario, output)
				}
				if _, err := os.Stat(call); !os.IsNotExist(err) {
					t.Errorf("gh was called with %s: %v", scenario, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("complete release failed: %v\n%s", err, output)
			}
			args := strings.Split(strings.TrimSuffix(string(read(t, call)), "\n"), "\n")
			for i, arg := range args {
				args[i] = strings.TrimPrefix(arg, "./")
			}
			want := append([]string{"release", "create", version}, archives...)
			want = append(want, "SHA256SUMS", "--repo", "example/project", "--verify-tag")
			if scenario == "tag notes" {
				want = append(want, "--notes-file", notesFile)
			} else {
				want = append(want, "--generate-notes")
			}
			want = append(want, "--prerelease")
			if !slices.Equal(args, want) {
				t.Errorf("gh args = %q, want %q", args, want)
			}
		})
	}
}

func workflowJob(t *testing.T, name string) string {
	t.Helper()
	_, job, ok := strings.Cut(string(read(t, root+"/.github/workflows/release.yml")), "\n  "+name+":\n")
	if !ok {
		t.Fatalf("release workflow is missing job %q", name)
	}
	return job
}

func workflowRunBlocks(t *testing.T, job string) string {
	t.Helper()
	blocks := regexp.MustCompile(`(?m)^        run: \|\n((?:          .*\n)+)`).FindAllStringSubmatch(job, -1)
	if len(blocks) == 0 {
		t.Fatal("release workflow job is missing its shell commands")
	}
	var script strings.Builder
	for _, block := range blocks {
		script.WriteString(block[1])
	}
	return script.String()
}

func requireCommand(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is required: %v", name, err)
	}
	return path
}

func recordingPath(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		recorder := "#!/bin/sh\nprintf '%s\\n' " + name + " \"$@\" > \"$RELEASE_CALL\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(recorder), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}
