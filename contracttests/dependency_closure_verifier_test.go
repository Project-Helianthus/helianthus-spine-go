package contracttests

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	verifierCLI     = "python3 scripts/verify_dependency_closure.py --repo . --manifest provenance/closure-manifest.json --inventory-output <path> --evidence-output <path>"
	canonicalEEBus  = "github.com/Project-Helianthus/helianthus-eebus-go"
	reviewedSpine   = "v0.7.1-helianthus.1"
	reviewedEEBus   = "v0.7.1-helianthus.1"
	privateSentinel = "PRIVATE-CONTENT-must-not-leak-8f2c6f71"
)

type closureFixtureFile struct {
	data       string
	executable bool
	symlink    string
}

type closureResult struct {
	err                 error
	stdout, stderr      []byte
	inventory, evidence []byte
}

type closureCase struct {
	name          string
	edit          func(map[string]closureFixtureFile)
	wantPass      bool
	wantPath      string
	wantClass     string
	wantReason    string
	secret        string
	deterministic bool
}

func TestDependencyClosureVerifierFixtures(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	if _, err := os.Stat(verifier); err != nil {
		t.Fatalf("dependency closure verifier is absent: %v; expected executable CLI: %s", err, verifierCLI)
	}

	cases := []closureCase{
		{name: "valid canonical fixture", wantPass: true, deterministic: true},
		{
			name: "unrelated third party pseudo version", wantPass: true,
			edit: replaceFixtureText("go.mod", "v0.0.0-20260716000000-0123456789ab", "v1.2.4-0.20260716000000-abcdefabcdef"),
		},
		{
			name: "canonical to canonical replace", wantPath: "go.mod", wantClass: "go_module", wantReason: "replace_directive",
			edit: appendFixtureText("go.mod", "\nreplace "+canonicalShip+" "+canonicalVer+" => "+canonicalShip+" "+canonicalVer+"\n"),
		},
		{
			name: "local filesystem replace", wantPath: "go.mod", wantClass: "go_module", wantReason: "replace_directive",
			edit: appendFixtureText("go.mod", "\nreplace "+canonicalShip+" => ./third_party/ship-go\n"),
		},
		{
			name: "workspace replace despite GOWORK off", wantPath: "go.work", wantClass: "workspace", wantReason: "replace_directive",
			edit: setFixtureText("go.work", "go 1.22.0\n\nreplace "+canonicalShip+" => "+canonicalShip+" "+canonicalVer+"\n"),
		},
		{
			name: "committed workspace use current module", wantPath: "go.work", wantClass: "workspace", wantReason: "workspace_local_selection",
			edit: setFixtureText("go.work", "go 1.22.0\n\nuse .\n"),
		},
		{
			name: "committed workspace use local module", wantPath: "go.work", wantClass: "workspace", wantReason: "workspace_local_selection",
			edit: setFixtureText("go.work", "go 1.22.0\n\nuse ./local/ship-go\n"),
		},
		{
			name: "unclassified tracked dependency control", wantPath: "dependency-control.lockx", wantClass: "unclassified", wantReason: "unclassified_dependency_control",
			edit: setFixtureText("dependency-control.lockx", "module="+canonicalShip+"\nversion="+canonicalVer+"\n"),
		},
		{
			name: "private contents stay private", wantPath: "release/release.json", wantClass: "release_config", wantReason: "upstream_module_identity", secret: privateSentinel,
			edit: setFixtureText("release/release.json", `{"private":"`+privateSentinel+`","module":"`+upstreamShip+`","release_inputs":["release/nested.json"]}`+"\n"),
		},
	}

	hidden := []struct{ name, path, class, data string }{
		{"go mod", "go.mod", "go_module", "\nrequire " + upstreamShip + " v0.6.0\n"},
		{"go work", "go.work", "workspace", "go 1.22.0\n\nreplace example.com/ship => " + upstreamShip + " v0.6.0\n"},
		{"go work sum", "go.work.sum", "workspace_checksum", upstreamShip + " v0.6.0/go.mod h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"},
		{"vendor manifest", "vendor/modules.txt", "vendor_manifest", "# " + upstreamShip + " v0.6.0\n"},
		{"workflow", ".github/workflows/release.yml", "workflow", "\n# " + upstreamShip + "\n"},
		{"local action", ".github/actions/release/action.yml", "local_action", "\n# " + upstreamShip + "\n"},
		{"release script", "scripts/release.sh", "build_release_control", "\n# " + upstreamShip + "\n"},
		{"release config", "release/release.json", "release_config", `{"module":"` + upstreamShip + `","release_inputs":["release/nested.json"]}` + "\n"},
		{"recursive release input", "release/nested.json", "release_config", `{"module":"` + upstreamShip + `"}` + "\n"},
		{"upstream eebus", "build/dependencies.json", "build_release_control", `{"module":"` + upstreamEEBus + `"}` + "\n"},
	}
	for _, item := range hidden {
		edit := setFixtureText(item.path, item.data)
		if _, exists := baseClosureFixture()[item.path]; exists && item.path != "release/release.json" && item.path != "release/nested.json" {
			edit = appendFixtureText(item.path, item.data)
		}
		cases = append(cases, closureCase{
			name: "upstream identity in " + item.name, edit: edit,
			wantPath: item.path, wantClass: item.class, wantReason: "upstream_module_identity",
		})
	}

	forks := []struct{ name, module, reviewed string }{
		{"ship", canonicalShip, canonicalVer},
		{"spine", canonicalModule, reviewedSpine},
		{"eebus", canonicalEEBus, reviewedEEBus},
	}
	for _, fork := range forks {
		badVersions := []struct{ name, version string }{
			{"pseudo version", strings.TrimSuffix(fork.reviewed, "-helianthus.1") + "-0.20260716000000-0123456789ab"},
			{"branch selection", "helianthus-v0.7"},
			{"main query", "main"},
			{"dev query", "dev"},
			{"latest query", "latest"},
			{"non reviewed tag", strings.TrimSuffix(fork.reviewed, ".1") + ".2"},
		}
		for _, bad := range badVersions {
			cases = append(cases, closureCase{
				name:     "unreviewed " + fork.name + " " + bad.name,
				edit:     replaceFixtureText("go.mod", fork.module+" "+fork.reviewed, fork.module+" "+bad.version),
				wantPath: "go.mod", wantClass: "go_module", wantReason: "unreviewed_project_fork_version",
			})
		}
	}

	surfaces := []struct{ name, path, class string }{
		{"nested go mod", "nested/module/go.mod", "go_module"},
		{"nested go sum", "nested/module/go.sum", "go_checksum"},
		{"nested go work", "nested/work/go.work", "workspace"},
		{"nested go work sum", "nested/work/go.work.sum", "workspace_checksum"},
		{"nested vendor manifest", "nested/vendor/modules.txt", "vendor_manifest"},
		{"arbitrary script", "scripts/check.py", "build_release_control"},
		{"build input", "build/closure.txt", "build_release_control"},
		{"release input", "release/closure.txt", "build_release_control"},
		{"config input", "config/closure.conf", "config"},
		{"root makefile", "Makefile", "makefile"},
		{"nested makefile", "tools/Makefile", "makefile"},
		{"make include", "tools/closure.mk", "makefile"},
		{"dockerfile", "Dockerfile.release", "container_build"},
		{"nested containerfile", "images/Containerfile.build", "container_build"},
		{"taskfile", "Taskfile.release.yml", "taskfile"},
		{"build yaml", "ci/package-build.yaml", "build_release_config"},
		{"release toml", "ci/package-release.toml", "build_release_config"},
		{"goreleaser", ".goreleaser.yaml", "build_release_config"},
		{"backup go source", "model/escaped.go_temp", "source_identity"},
	}
	for _, surface := range surfaces {
		cases = append(cases, closureCase{
			name:     "closed surface " + surface.name,
			edit:     setFixtureText(surface.path, upstreamShip+"\n"),
			wantPath: surface.path, wantClass: surface.class, wantReason: "upstream_module_identity",
		})
	}

	cases = append(cases,
		closureCase{
			name: "structured JSON split module and version", edit: setFixtureText("release/release.json", `{"module":"`+canonicalShip+`","version":"main"}`+"\n"),
			wantPath: "release/release.json", wantClass: "release_config", wantReason: "unreviewed_project_fork_version",
		},
		closureCase{
			name: "structured YAML split module and version", edit: setFixtureText("config/dependency.yml", "module: "+canonicalShip+"\nversion: latest\n"),
			wantPath: "config/dependency.yml", wantClass: "config", wantReason: "unreviewed_project_fork_version",
		},
		closureCase{
			name: "module query syntax", edit: appendFixtureText("scripts/release.sh", "\n# "+canonicalShip+"?ref=dev\n"),
			wantPath: "scripts/release.sh", wantClass: "build_release_control", wantReason: "unreviewed_project_fork_version",
		},
		closureCase{
			name: "recursive JSON local reference", edit: func(files map[string]closureFixtureFile) {
				files["release/release.json"] = closureFixtureFile{data: `{"release_inputs":["assets/nested.json"]}` + "\n"}
				files["assets/nested.json"] = closureFixtureFile{data: `{"module":"` + upstreamShip + `"}` + "\n"}
			},
			wantPath: "assets/nested.json", wantClass: "referenced_input", wantReason: "upstream_module_identity",
		},
		closureCase{
			name: "relative recursive JSON local reference", edit: func(files map[string]closureFixtureFile) {
				files["release/release.json"] = closureFixtureFile{data: `{"release_inputs":["../assets/nested.json"]}` + "\n"}
				files["assets/nested.json"] = closureFixtureFile{data: `{"module":"` + upstreamShip + `"}` + "\n"}
			},
			wantPath: "assets/nested.json", wantClass: "referenced_input", wantReason: "upstream_module_identity",
		},
		closureCase{
			name: "tracked directory prefix expansion", edit: func(files map[string]closureFixtureFile) {
				files["release/release.json"] = closureFixtureFile{data: `{"release_inputs":["assets/bundle"]}` + "\n"}
				files["assets/bundle/nested.json"] = closureFixtureFile{data: `{"module":"` + upstreamShip + `"}` + "\n"}
			},
			wantPath: "assets/bundle/nested.json", wantClass: "referenced_input", wantReason: "upstream_module_identity",
		},
		closureCase{
			name: "recursive script local reference", edit: func(files map[string]closureFixtureFile) {
				files["scripts/release.sh"] = closureFixtureFile{data: "#!/bin/sh\ncat assets/module.txt\n", executable: true}
				files["assets/module.txt"] = closureFixtureFile{data: upstreamShip + "\n"}
			},
			wantPath: "assets/module.txt", wantClass: "referenced_input", wantReason: "upstream_module_identity",
		},
		closureCase{
			name: "recursive YAML local reference", edit: func(files map[string]closureFixtureFile) {
				files["config/dependency.yml"] = closureFixtureFile{data: "input: assets/module.json\n"}
				files["assets/module.json"] = closureFixtureFile{data: `{"module":"` + upstreamShip + `"}` + "\n"}
			},
			wantPath: "assets/module.json", wantClass: "referenced_input", wantReason: "upstream_module_identity",
		},
		closureCase{
			name: "untracked local reference", edit: setFixtureText("release/release.json", `{"release_inputs":["assets/missing.json"]}`+"\n"),
			wantPath: "release/release.json", wantClass: "release_config", wantReason: "referenced_input_untracked",
		},
		closureCase{
			name: "outside repository reference", edit: setFixtureText("release/release.json", `{"release_inputs":["../../private.json"]}`+"\n"),
			wantPath: "release/release.json", wantClass: "release_config", wantReason: "reference_outside_repo",
		},
		closureCase{
			name: "malformed declared JSON", edit: setFixtureText("release/release.json", "{\"release_inputs\":[\n"),
			wantPath: "release/release.json", wantClass: "release_config", wantReason: "malformed_control_input",
		},
		closureCase{
			name: "malformed YAML control", edit: setFixtureText("config/dependency.yml", "inputs: [unterminated\n"),
			wantPath: "config/dependency.yml", wantClass: "config", wantReason: "malformed_control_input",
		},
		closureCase{
			name: "tracked symlink", edit: func(files map[string]closureFixtureFile) {
				files["build/dependency-link"] = closureFixtureFile{symlink: "../go.mod"}
			},
			wantPath: "build/dependency-link", wantClass: "build_release_control", wantReason: "tracked_symlink",
		},
	)

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := writeClosureFixture(t, test.edit)
			result := runFixtureVerifier(t, verifier, root)
			assertFixtureResult(t, root, result, test)
			if test.deterministic {
				again := runFixtureVerifier(t, verifier, root)
				assertFixtureResult(t, root, again, test)
				if !bytes.Equal(result.inventory, again.inventory) || !bytes.Equal(result.evidence, again.evidence) {
					t.Error("identical runs produced different inventory or evidence")
				}
			}
		})
	}
}

func baseClosureFixture() map[string]closureFixtureFile {
	return map[string]closureFixtureFile{
		".github/actions/release/action.yml": {data: "name: Release\nruns:\n  using: composite\n  steps:\n    - shell: bash\n      run: scripts/release.sh release/release.json\n"},
		".github/workflows/release.yml":      {data: "name: Release\njobs:\n  release:\n    steps:\n      - uses: ./.github/actions/release\n"},
		"go.mod": {data: `module github.com/Project-Helianthus/dependency-closure-fixture

go 1.22.0

require (
	github.com/Project-Helianthus/helianthus-ship-go v0.6.1-helianthus.1
	github.com/Project-Helianthus/helianthus-spine-go v0.7.1-helianthus.1
	github.com/Project-Helianthus/helianthus-eebus-go v0.7.1-helianthus.1
	example.com/unrelated v0.0.0-20260716000000-0123456789ab
)
`},
		"go.sum":  {data: "github.com/Project-Helianthus/helianthus-ship-go v0.6.1-helianthus.1/go.mod h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"},
		"main.go": {data: "package fixture\n\nimport _ \"github.com/Project-Helianthus/helianthus-ship-go/api\"\n"},
		"provenance/closure-manifest.json": {data: `{
  "dependency_control_inputs": [
    ".github/actions/release/action.yml",
    ".github/workflows/release.yml",
    "scripts/release.sh",
    "release/release.json"
  ],
  "downstream_patches": [],
  "fork": {
    "intended_prerelease": "v0.0.1-helianthus.1",
    "lifecycle": "temporary_downstream_patch_carrier",
    "origin": "https://github.com/Project-Helianthus/dependency-closure-fixture.git"
  },
  "license": {
    "path": "LICENSE",
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  },
  "module": "github.com/Project-Helianthus/dependency-closure-fixture",
  "notice_inventory": [],
  "reviewed_dependencies": [
    {
      "license": {"path": "LICENSE", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
      "module": "github.com/Project-Helianthus/helianthus-eebus-go",
      "peeled_commit_sha": "1111111111111111111111111111111111111111",
      "provenance_manifest": {"path": "provenance/closure-manifest.json", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
      "tag_object_sha": "2222222222222222222222222222222222222222",
      "tree_sha": "3333333333333333333333333333333333333333",
      "version": "v0.7.1-helianthus.1"
    },
    {
      "license": {"path": "LICENSE", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
      "module": "github.com/Project-Helianthus/helianthus-ship-go",
      "peeled_commit_sha": "4444444444444444444444444444444444444444",
      "provenance_manifest": {"path": "provenance/closure-manifest.json", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
      "tag_object_sha": "5555555555555555555555555555555555555555",
      "tree_sha": "6666666666666666666666666666666666666666",
      "version": "v0.6.1-helianthus.1"
    },
    {
      "license": {"path": "LICENSE", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
      "module": "github.com/Project-Helianthus/helianthus-spine-go",
      "peeled_commit_sha": "7777777777777777777777777777777777777777",
      "provenance_manifest": {"path": "provenance/closure-manifest.json", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
      "tag_object_sha": "8888888888888888888888888888888888888888",
      "tree_sha": "9999999999999999999999999999999999999999",
      "version": "v0.7.1-helianthus.1"
    }
  ],
  "reviewed_patches": [
    {
      "files": ["main.go"],
      "head_commit_sha": "dddddddddddddddddddddddddddddddddddddddd",
      "merge_commit_sha": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
      "upstream_issue": "https://example.invalid/upstream/issues/1",
      "upstream_pr": "https://example.invalid/upstream/pull/2"
    }
  ],
  "schema": "helianthus.provenance.closure-manifest.v1",
  "source_header_inventory": {"globs": ["**/*.go"], "headers": []},
  "upstream": {
    "peeled_commit_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "remote": "https://example.invalid/upstream.git",
    "tag": "v0.0.0",
    "tag_object_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "tree_sha": "cccccccccccccccccccccccccccccccccccccccc"
  }
}
`},
		"LICENSE":              {data: "fixture license\n"},
		"release/nested.json":  {data: `{"module":"github.com/Project-Helianthus/helianthus-ship-go@v0.6.1-helianthus.1"}` + "\n"},
		"release/release.json": {data: `{"release_inputs":["release/nested.json"]}` + "\n"},
		"scripts/release.sh":   {data: "#!/bin/sh\nset -eu\ntest -f \"${1:?release config required}\"\n", executable: true},
		"vendor/modules.txt":   {data: "# github.com/Project-Helianthus/helianthus-ship-go v0.6.1-helianthus.1\n## explicit; go 1.22\ngithub.com/Project-Helianthus/helianthus-ship-go/api\n"},
	}
}

func setFixtureText(path, data string) func(map[string]closureFixtureFile) {
	return func(files map[string]closureFixtureFile) { files[path] = closureFixtureFile{data: data} }
}

func appendFixtureText(path, data string) func(map[string]closureFixtureFile) {
	return func(files map[string]closureFixtureFile) {
		file := files[path]
		file.data += data
		files[path] = file
	}
}

func replaceFixtureText(path, old, new string) func(map[string]closureFixtureFile) {
	return func(files map[string]closureFixtureFile) {
		file := files[path]
		file.data = strings.Replace(file.data, old, new, 1)
		files[path] = file
	}
}

func writeClosureFixture(t *testing.T, edit func(map[string]closureFixtureFile)) string {
	t.Helper()
	root := t.TempDir()
	files := baseClosureFixture()
	if edit != nil {
		edit(files)
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := files[path]
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if file.executable {
			mode = 0o755
		}
		if file.symlink != "" {
			if err := os.Symlink(file.symlink, fullPath); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(fullPath, []byte(file.data), mode); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, root, "init", "-q")
	runFixtureGit(t, root, "add", "--all")
	runFixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "-q", "-m", "fixture")
	return root
}

func runFixtureGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-07-16T00:00:00Z", "GIT_COMMITTER_DATE=2026-07-16T00:00:00Z")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func runFixtureVerifier(t *testing.T, verifier, root string) closureResult {
	t.Helper()
	outputDir := t.TempDir()
	inventoryPath := filepath.Join(outputDir, "tracked.nul")
	evidencePath := filepath.Join(outputDir, "evidence.json")
	cmd := exec.Command("python3", verifier, "--repo", ".", "--manifest", "provenance/closure-manifest.json", "--inventory-output", inventoryPath, "--evidence-output", evidencePath)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return closureResult{err: err, stdout: stdout.Bytes(), stderr: stderr.Bytes(), inventory: readFixtureOutput(t, inventoryPath), evidence: readFixtureOutput(t, evidencePath)}
}

func readFixtureOutput(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err == nil {
		return data
	}
	if os.IsNotExist(err) {
		return nil
	}
	t.Fatal(err)
	return nil
}

func assertFixtureResult(t *testing.T, root string, result closureResult, test closureCase) {
	t.Helper()
	wantInventory := fixtureInventory(t, root)
	if !bytes.Equal(result.inventory, wantInventory) || len(result.inventory) == 0 || result.inventory[len(result.inventory)-1] != 0 {
		t.Errorf("inventory is not exact NUL-delimited git ls-files output: got %q want %q", result.inventory, wantInventory)
	}
	evidence := decodeCanonicalEvidence(t, result.evidence)
	digest := sha256.Sum256(wantInventory)
	inventory, ok := evidence["tracked_inventory"].(map[string]any)
	if evidence["schema"] != "helianthus.dependency-closure-evidence.v2" || !ok || inventory["sha256"] != fmt.Sprintf("%x", digest) {
		t.Errorf("evidence identity/digest mismatch: %s", result.evidence)
	}
	assertEvidenceFieldsAreStable(t, evidence)

	if test.wantPass {
		if result.err != nil || evidence["result"] != "pass" {
			t.Fatalf("verifier rejected valid fixture: %v\nstdout=%s\nstderr=%s\nevidence=%s", result.err, result.stdout, result.stderr, result.evidence)
		}
		if violations, ok := evidence["violations"].([]any); !ok || len(violations) != 0 {
			t.Errorf("passing evidence violations = %v; want empty array", evidence["violations"])
		}
		if len(result.stdout) != 0 || len(result.stderr) != 0 {
			t.Errorf("passing verifier must be quiet: stdout=%q stderr=%q", result.stdout, result.stderr)
		}
		return
	}

	if result.err == nil || evidence["result"] != "fail" {
		t.Fatalf("verifier accepted forbidden fixture; want reason=%s path=%s class=%s", test.wantReason, test.wantPath, test.wantClass)
	}
	wantDiagnostic := fmt.Sprintf("dependency-closure: FAIL reason=%s path=%s class=%s", test.wantReason, test.wantPath, test.wantClass)
	if len(result.stdout) != 0 || !strings.Contains(string(result.stderr), wantDiagnostic) {
		t.Errorf("diagnostics do not contain stable rejection %q: stdout=%q stderr=%q", wantDiagnostic, result.stdout, result.stderr)
	}
	linePattern := regexp.MustCompile(`^dependency-closure: FAIL reason=[a-z0-9_]+ path=[A-Za-z0-9_./-]+ class=[a-z0-9_]+$`)
	for _, line := range strings.Split(strings.TrimSpace(string(result.stderr)), "\n") {
		if !linePattern.MatchString(line) {
			t.Errorf("diagnostic contains data beyond reason/path/class: %q", line)
		}
	}
	if bytes.Contains(result.stderr, []byte(root)) {
		t.Error("diagnostics contain unstable absolute fixture path")
	}
	foundViolation := false
	if violations, ok := evidence["violations"].([]any); ok {
		for _, value := range violations {
			violation, ok := value.(map[string]any)
			if ok && violation["reason"] == test.wantReason && violation["path"] == test.wantPath && violation["class"] == test.wantClass {
				foundViolation = true
			}
		}
	}
	if !foundViolation {
		t.Errorf("evidence lacks reason=%s path=%s class=%s: %s", test.wantReason, test.wantPath, test.wantClass, result.evidence)
	}
	if test.secret != "" {
		for name, output := range map[string][]byte{"stdout": result.stdout, "stderr": result.stderr, "inventory": result.inventory, "evidence": result.evidence} {
			if bytes.Contains(output, []byte(test.secret)) {
				t.Errorf("%s discloses private file contents", name)
			}
		}
	}
}

func decodeCanonicalEvidence(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var evidence map[string]any
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("evidence is not JSON: %v\n%s", err, data)
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		t.Errorf("evidence is not compact sorted-key JSON with one trailing newline: got %q want %q", data, canonical)
	}
	return evidence
}

func assertEvidenceFieldsAreStable(t *testing.T, evidence map[string]any) {
	t.Helper()
	wantFields := []string{"commands", "inputs", "manifest", "result", "schema", "source_sha", "tracked_inventory", "verifier", "violations"}
	if len(evidence) != len(wantFields) {
		t.Errorf("evidence has unexpected top-level shape: %v", evidence)
	}
	for _, field := range wantFields {
		if _, ok := evidence[field]; !ok {
			t.Errorf("evidence lacks %s", field)
		}
	}
	sourceSHA, ok := evidence["source_sha"].(string)
	if !ok || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sourceSHA) {
		t.Errorf("evidence source_sha = %v; want full commit", evidence["source_sha"])
	}
	for _, field := range []string{"inputs", "violations"} {
		values, ok := evidence[field].([]any)
		if !ok {
			t.Errorf("evidence %s is %T, want array", field, evidence[field])
			continue
		}
		for _, value := range values {
			object, ok := value.(map[string]any)
			if !ok {
				t.Errorf("evidence %s entry is %T, want object", field, value)
				continue
			}
			wantKeys := 4
			if field == "violations" {
				wantKeys = 3
			}
			if len(object) != wantKeys || object["path"] == nil || object["class"] == nil || field == "violations" && object["reason"] == nil || field == "inputs" && (object["sha256"] == nil || object["source_sha"] != sourceSHA) {
				t.Errorf("evidence %s entry has an unstable shape: %v", field, object)
			}
		}
	}
	for _, field := range []string{"manifest", "verifier", "tracked_inventory"} {
		object, ok := evidence[field].(map[string]any)
		if !ok || object["sha256"] == nil || object["source_sha"] != sourceSHA {
			t.Errorf("evidence %s digest is not source-bound: %v", field, evidence[field])
		}
	}
	commands, ok := evidence["commands"].([]any)
	if !ok || len(commands) != 5 {
		t.Errorf("evidence commands = %v; want five versioned commands", evidence["commands"])
	}
}

func fixtureInventory(t *testing.T, root string) []byte {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return output
}
