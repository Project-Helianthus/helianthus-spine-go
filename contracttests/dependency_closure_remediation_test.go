package contracttests

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependencyClosureEvidenceBindsContentAndSource(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	firstRoot := writeClosureFixture(t, nil)
	secondRoot := writeClosureFixture(t, setFixtureText("release/nested.json", `{"note":"content mutation","module":"`+canonicalShip+`@`+canonicalVer+`"}`+"\n"))
	first := runFixtureVerifier(t, verifier, firstRoot)
	second := runFixtureVerifier(t, verifier, secondRoot)
	assertFixtureResult(t, firstRoot, first, closureCase{wantPass: true})
	assertFixtureResult(t, secondRoot, second, closureCase{wantPass: true})

	if !bytes.Equal(first.inventory, second.inventory) {
		t.Fatal("content-only mutation changed the tracked path inventory")
	}
	if bytes.Equal(first.evidence, second.evidence) {
		t.Fatal("content-only mutation did not change closure evidence")
	}
	firstEvidence := decodeCanonicalEvidence(t, first.evidence)
	secondEvidence := decodeCanonicalEvidence(t, second.evidence)
	if firstEvidence["source_sha"] == secondEvidence["source_sha"] {
		t.Error("content mutation did not change the exact fixture source SHA")
	}
	firstDigest := evidenceInputDigest(t, firstEvidence, "release/nested.json")
	secondDigest := evidenceInputDigest(t, secondEvidence, "release/nested.json")
	if firstDigest == secondDigest {
		t.Error("content mutation did not change the scanned input digest")
	}
}

func TestDependencyClosureManifestSchemaFailsClosed(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	cases := []closureCase{
		{
			name:     "unknown manifest field",
			edit:     replaceFixtureText("provenance/closure-manifest.json", "{\n", "{\n  \"unknown\": true,\n"),
			wantPath: "provenance/closure-manifest.json", wantClass: "provenance", wantReason: "invalid_manifest_schema",
		},
		{
			name:     "missing manifest field",
			edit:     replaceFixtureText("provenance/closure-manifest.json", "  \"notice_inventory\": [],\n", ""),
			wantPath: "provenance/closure-manifest.json", wantClass: "provenance", wantReason: "invalid_manifest_schema",
		},
		{
			name:     "malformed manifest",
			edit:     setFixtureText("provenance/closure-manifest.json", "{\n"),
			wantPath: "provenance/closure-manifest.json", wantClass: "provenance", wantReason: "invalid_manifest",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := writeClosureFixture(t, test.edit)
			assertFixtureResult(t, root, runFixtureVerifier(t, verifier, root), test)
		})
	}
}

func TestDependencyClosureBindsDownstreamPatchContent(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	t.Run("valid patch", func(t *testing.T) {
		root := writeClosureFixtureWithDownstreamPatch(t)
		assertFixtureResult(t, root, runFixtureVerifier(t, verifier, root), closureCase{wantPass: true})
	})
	tests := []struct {
		name   string
		mutate func(map[string]any)
		reason string
	}{
		{
			name: "forged digest",
			mutate: func(patch map[string]any) {
				patch["patch_sha256"] = strings.Repeat("0", 64)
			},
			reason: "downstream_patch_digest_mismatch",
		},
		{
			name: "missing commit",
			mutate: func(patch map[string]any) {
				patch["head_commit_sha"] = strings.Repeat("0", 40)
			},
			reason: "downstream_patch_commit_unavailable",
		},
		{
			name: "mismatched files",
			mutate: func(patch map[string]any) {
				patch["files"] = []any{"main.go"}
			},
			reason: "downstream_patch_files_mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeClosureFixtureWithDownstreamPatch(t)
			mutateFixtureDownstreamPatch(t, root, test.mutate)
			assertFixtureResult(t, root, runFixtureVerifier(t, verifier, root), closureCase{
				wantPath:   "provenance/closure-manifest.json",
				wantClass:  "provenance",
				wantReason: test.reason,
			})
		})
	}
}

func TestDependencyClosureRejectsContentNotAtHead(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	root := writeClosureFixture(t, nil)
	path := filepath.Join(root, "release", "nested.json")
	if err := os.WriteFile(path, []byte(`{"module":"`+canonicalShip+`@`+canonicalVer+`","dirty":true}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := runFixtureVerifier(t, verifier, root)
	if result.err == nil || !bytes.Contains(result.stderr, []byte("reason=tracked_content_not_at_head")) {
		t.Fatalf("dirty tracked content was not rejected: err=%v stderr=%q", result.err, result.stderr)
	}
	decodeCanonicalEvidence(t, result.evidence)
}

func writeClosureFixtureWithDownstreamPatch(t *testing.T) string {
	t.Helper()
	root := writeClosureFixture(t, nil)
	path := filepath.Join(root, "release", "nested.json")
	if err := os.WriteFile(path, []byte("{\"patched\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, root, "add", "release/nested.json")
	runFixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "-q", "-m", "downstream patch")
	commit := strings.TrimSpace(runFixtureGitOutput(t, root, "rev-parse", "HEAD"))
	patch := runFixtureGitOutput(t, root, "show", "--format=", "--no-ext-diff", "--binary", commit, "--", "release/nested.json")
	digest := sha256.Sum256([]byte(patch))

	mutateFixtureDownstreamPatch(t, root, func(record map[string]any) {
		record["files"] = []any{"release/nested.json"}
		record["head_commit_sha"] = commit
		record["issue"] = "https://github.com/Project-Helianthus/dependency-closure-fixture/issues/1"
		record["patch_sha256"] = fmt.Sprintf("%x", digest)
		record["pull_request"] = "https://github.com/Project-Helianthus/dependency-closure-fixture/pull/2"
	})
	return root
}

func mutateFixtureDownstreamPatch(t *testing.T, root string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, "provenance", "closure-manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	patches, _ := manifest["downstream_patches"].([]any)
	var patch map[string]any
	if len(patches) == 0 {
		patch = make(map[string]any)
		patches = append(patches, patch)
		manifest["downstream_patches"] = patches
	} else {
		patch, _ = patches[0].(map[string]any)
	}
	mutate(patch)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, root, "add", "provenance/closure-manifest.json")
	runFixtureGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "-q", "-m", "bind downstream patch")
}

func runFixtureGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(output)
}

func TestDependencyClosureRejectsNonportableTrackedNames(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	for _, path := range []string{"build/bad-é.yml", "build/bad\nname.yml"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			root := writeClosureFixture(t, setFixtureText(path, "private\n"))
			result := runFixtureVerifier(t, verifier, root)
			if result.err == nil || !bytes.Contains(result.stderr, []byte("reason=nonportable_tracked_path")) {
				t.Fatalf("nonportable path was not rejected: err=%v stderr=%q", result.err, result.stderr)
			}
			if bytes.Contains(result.stderr, []byte(root)) || bytes.Contains(result.stderr, []byte("private")) || bytes.Contains(result.stderr, []byte("\nname")) {
				t.Errorf("bounded diagnostics leaked path or content: %q", result.stderr)
			}
			decodeCanonicalEvidence(t, result.evidence)
		})
	}
}

func TestDependencyClosureBoundsGitAndReadFailures(t *testing.T) {
	verifier := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	t.Run("non git repository", func(t *testing.T) {
		result := runVerifierAt(t, verifier, t.TempDir(), os.Environ())
		assertBoundedFailure(t, result, "git_inventory_failed")
	})
	t.Run("git unavailable", func(t *testing.T) {
		root := writeClosureFixture(t, nil)
		env := replaceEnvironment(os.Environ(), "PATH", "/nonexistent")
		result := runVerifierAt(t, verifier, root, env)
		assertBoundedFailure(t, result, "git_unavailable")
	})
	t.Run("unreadable control", func(t *testing.T) {
		root := writeClosureFixture(t, setFixtureText("build/private.json", "{}\n"))
		path := filepath.Join(root, "build", "private.json")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		result := runFixtureVerifier(t, verifier, root)
		if result.err == nil || !bytes.Contains(result.stderr, []byte("reason=unreadable_input")) {
			t.Fatalf("unreadable control was not rejected: err=%v stderr=%q", result.err, result.stderr)
		}
	})
}

func evidenceInputDigest(t *testing.T, evidence map[string]any, path string) string {
	t.Helper()
	inputs, ok := evidence["inputs"].([]any)
	if !ok {
		t.Fatalf("evidence inputs = %T", evidence["inputs"])
	}
	for _, value := range inputs {
		input, ok := value.(map[string]any)
		if ok && input["path"] == path {
			digest, _ := input["sha256"].(string)
			return digest
		}
	}
	t.Fatalf("evidence lacks input %s", path)
	return ""
}

func runVerifierAt(t *testing.T, verifier, root string, env []string) closureResult {
	t.Helper()
	outputDir := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, verifier, "--repo", ".", "--manifest", "provenance/closure-manifest.json", "--inventory-output", filepath.Join(outputDir, "tracked.nul"), "--evidence-output", filepath.Join(outputDir, "evidence.json"))
	cmd.Dir = root
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	return closureResult{
		err: err, stdout: stdout.Bytes(), stderr: stderr.Bytes(),
		inventory: readFixtureOutput(t, filepath.Join(outputDir, "tracked.nul")),
		evidence:  readFixtureOutput(t, filepath.Join(outputDir, "evidence.json")),
	}
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func assertBoundedFailure(t *testing.T, result closureResult, reason string) {
	t.Helper()
	if result.err == nil || !bytes.Contains(result.stderr, []byte("reason="+reason)) {
		t.Fatalf("bounded failure reason %s absent: err=%v stderr=%q", reason, result.err, result.stderr)
	}
	if bytes.Contains(result.stderr, []byte("Traceback")) || len(result.stderr) > 4096 {
		t.Errorf("failure diagnostics are unbounded: %q", result.stderr)
	}
	decodeCanonicalEvidence(t, result.evidence)
}
