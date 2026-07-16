package contracttests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	verifierCLI     = "scripts/verify_dependency_closure.py --root <git-repository> --inventory-out <path> --evidence-out <path>"
	canonicalEEBus  = "github.com/Project-Helianthus/helianthus-eebus-go"
	reviewedSpine   = "v0.7.1-helianthus.1"
	reviewedEEBus   = "v0.7.1-helianthus.1"
	privateSentinel = "PRIVATE-CONTENT-must-not-leak-8f2c6f71"
)

type closureFixtureFile struct {
	data       string
	executable bool
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
			name: "unclassified tracked release control", wantPath: "release/dependency-control.lockx", wantClass: "unclassified", wantReason: "unclassified_dependency_control",
			edit: setFixtureText("release/dependency-control.lockx", "module="+canonicalShip+"\nversion="+canonicalVer+"\n"),
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
		{"recursive release input", "release/nested.json", "release_input", `{"module":"` + upstreamShip + `"}` + "\n"},
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
		"go.sum":               {data: "github.com/Project-Helianthus/helianthus-ship-go v0.6.1-helianthus.1/go.mod h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"},
		"main.go":              {data: "package fixture\n\nimport _ \"github.com/Project-Helianthus/helianthus-ship-go/api\"\n"},
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
		if err := os.WriteFile(fullPath, []byte(file.data), mode); err != nil {
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
	cmd := exec.Command(verifier, "--root", root, "--inventory-out", inventoryPath, "--evidence-out", evidencePath)
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
	if evidence["schema"] != "helianthus.dependency-closure-evidence.v1" || evidence["tracked_inventory_sha256"] != hex.EncodeToString(digest[:]) {
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
	if len(evidence) != 5 {
		t.Errorf("evidence must contain only inputs, result, schema, tracked_inventory_sha256, violations: %v", evidence)
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
			wantKeys := 2
			if field == "violations" {
				wantKeys = 3
			}
			if len(object) != wantKeys || object["path"] == nil || object["class"] == nil || field == "violations" && object["reason"] == nil {
				t.Errorf("evidence %s entry must contain only stable path/class/reason fields: %v", field, object)
			}
		}
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
