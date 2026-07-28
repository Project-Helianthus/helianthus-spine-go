package contracttests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	canonicalModule = "github.com/Project-Helianthus/helianthus-spine-go"
	canonicalShip   = "github.com/Project-Helianthus/helianthus-ship-go"
	canonicalVer    = "v0.6.1-helianthus.1"
	upstreamSpine   = "github.com/enbility/spine-go"
	upstreamShip    = "github.com/enbility/ship-go"
	upstreamEEBus   = "github.com/enbility/eebus-go"
	productionHash  = "ba371628f2a16b008951e054e8cb539f5814088f98166c0401026e0d82b8c6ed"
)

func TestModuleDependencyClosure(t *testing.T) {
	root := repositoryRoot(t)
	goMod := string(readFile(t, filepath.Join(root, "go.mod")))
	moduleLine := regexp.MustCompile(`(?m)^module\s+(\S+)\s*$`).FindStringSubmatch(goMod)
	if len(moduleLine) != 2 || moduleLine[1] != canonicalModule {
		t.Errorf("module directive = %q; want %q", moduleLine, canonicalModule)
	}
	directShip := regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(canonicalShip) + `\s+` + regexp.QuoteMeta(canonicalVer) + `\s*$`)
	if !directShip.MatchString(goMod) {
		t.Errorf("go.mod lacks direct reviewed dependency %s %s", canonicalShip, canonicalVer)
	}
	if strings.Contains(goMod, upstreamShip) {
		t.Errorf("go.mod still contains forbidden upstream dependency %s", upstreamShip)
	}
	if regexp.MustCompile(`(?m)^\s*replace(?:\s|\()`).MatchString(goMod) {
		t.Error("go.mod contains a replace directive; canonical closure must not use replace")
	}

	cmd := exec.Command("go", "list", "-m", "-mod=readonly", "all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("inspect module graph: %v", err)
	}
	graph := string(out)
	if strings.Contains(graph, upstreamShip+" ") || strings.Contains(graph, upstreamShip+"\n") {
		t.Errorf("module graph still contains forbidden %s", upstreamShip)
	}
	if !strings.Contains(graph, canonicalShip+" "+canonicalVer+"\n") {
		t.Errorf("module graph does not contain reviewed dependency %s %s", canonicalShip, canonicalVer)
	}
}
func TestTrackedGoImportsUseCanonicalIdentity(t *testing.T) {
	root := repositoryRoot(t)
	var violations []string
	canonicalCounts := map[string]int{canonicalModule: 0, canonicalShip: 0}
	for _, path := range trackedGoSourceFiles(t, root) {
		src := readFile(t, filepath.Join(root, path))
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports in %s: %v", path, err)
		}
		for _, spec := range f.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("decode import in %s: %v", path, err)
			}
			switch {
			case hasImportPrefix(importPath, upstreamSpine):
				violations = append(violations, fmt.Sprintf("%s: %s -> %s", path, importPath, canonicalModule+strings.TrimPrefix(importPath, upstreamSpine)))
			case hasImportPrefix(importPath, upstreamShip):
				violations = append(violations, fmt.Sprintf("%s: %s -> %s", path, importPath, canonicalShip+strings.TrimPrefix(importPath, upstreamShip)))
			case hasImportPrefix(importPath, canonicalModule):
				canonicalCounts[canonicalModule]++
			case hasImportPrefix(importPath, canonicalShip):
				canonicalCounts[canonicalShip]++
			}
		}
	}
	if len(violations) != 0 {
		shown := violations
		if len(shown) > 12 {
			shown = shown[:12]
		}
		t.Errorf("found %d non-canonical tracked Go imports (first %d):\n%s", len(violations), len(shown), strings.Join(shown, "\n"))
	}
	for _, module := range []string{canonicalModule, canonicalShip} {
		if canonicalCounts[module] == 0 {
			t.Errorf("no tracked Go import uses required canonical prefix %s", module)
		}
	}
}
func TestProvenanceManifestBindsUpstream(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "provenance", "closure-manifest.json")
	var manifest struct {
		Schema string `json:"schema"`
		Module string `json:"module"`
		Fork   struct {
			Origin             string `json:"origin"`
			Lifecycle          string `json:"lifecycle"`
			IntendedPrerelease string `json:"intended_prerelease"`
		} `json:"fork"`
		Upstream struct {
			Remote    string `json:"remote"`
			Tag       string `json:"tag"`
			TagObject string `json:"tag_object_sha"`
			Commit    string `json:"peeled_commit_sha"`
			Tree      string `json:"tree_sha"`
		} `json:"upstream"`
		License struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"license"`
		NoticeInventory []string `json:"notice_inventory"`
		SourceHeaders   struct {
			Globs   []string `json:"globs"`
			Headers []string `json:"headers"`
		} `json:"source_header_inventory"`
		ReviewedDependencies []struct {
			Module  string `json:"module"`
			Version string `json:"version"`
			Tag     string `json:"tag_object_sha"`
			Commit  string `json:"peeled_commit_sha"`
			Tree    string `json:"tree_sha"`
			License struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			} `json:"license"`
			Manifest struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			} `json:"provenance_manifest"`
		} `json:"reviewed_dependencies"`
		DownstreamPatches []struct {
			BaseCommit    string   `json:"base_commit_sha"`
			ContentSHA256 string   `json:"content_sha256"`
			Files         []string `json:"files"`
			Issue         string   `json:"issue"`
			PatchSHA256   string   `json:"patch_sha256"`
			PullRequest   string   `json:"pull_request"`
		} `json:"downstream_patches"`
		ReviewedPatches []struct {
			Files       []string `json:"files"`
			HeadCommit  string   `json:"head_commit_sha"`
			MergeCommit string   `json:"merge_commit_sha"`
			Issue       string   `json:"upstream_issue"`
			PullRequest string   `json:"upstream_pr"`
		} `json:"reviewed_patches"`
		DependencyControlInputs []string `json:"dependency_control_inputs"`
	}
	data := readFile(t, path)
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	wants := []struct{ name, got, want string }{
		{"schema", manifest.Schema, "helianthus.provenance.closure-manifest.v2"},
		{"module", manifest.Module, canonicalModule},
		{"fork.origin", manifest.Fork.Origin, "https://github.com/Project-Helianthus/helianthus-spine-go.git"},
		{"fork.lifecycle", manifest.Fork.Lifecycle, "temporary_downstream_patch_carrier"},
		{"fork.intended_prerelease", manifest.Fork.IntendedPrerelease, "v0.7.1-helianthus.4"},
		{"upstream.remote", manifest.Upstream.Remote, "https://github.com/enbility/spine-go.git"},
		{"upstream.tag", manifest.Upstream.Tag, "v0.7.0"},
		{"upstream.tag_object_sha", manifest.Upstream.TagObject, "30aeb9ac51c3212d280acd93a9afaf58bc63bd92"},
		{"upstream.peeled_commit_sha", manifest.Upstream.Commit, "0eef075cb6e8f697355a2850344333452f5590cf"},
		{"upstream.tree_sha", manifest.Upstream.Tree, "e070b8272e357643ec855c4eab22be4dd03cb89b"},
		{"license.path", manifest.License.Path, "LICENSE"},
		{"license.sha256", manifest.License.SHA256, "c853996135802c50b3048937e48022bc00b41ff5f56a31cebe7d686bf91f87db"},
	}
	for _, check := range wants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	if len(manifest.NoticeInventory) != 0 || len(manifest.SourceHeaders.Headers) != 0 || !strings.EqualFold(strings.Join(manifest.SourceHeaders.Globs, ","), "**/*.go") {
		t.Errorf("manifest notice/source-header inventories are not explicitly closed: notices=%v source_headers=%v", manifest.NoticeInventory, manifest.SourceHeaders)
	}
	if len(manifest.ReviewedDependencies) != 1 {
		t.Fatalf("manifest reviewed_dependencies = %d; want exactly canonical SHIP", len(manifest.ReviewedDependencies))
	}
	if len(manifest.ReviewedPatches) != 1 {
		t.Fatalf("manifest reviewed_patches = %d; want exactly one upstream race fix", len(manifest.ReviewedPatches))
	}
	if len(manifest.DownstreamPatches) != 5 {
		t.Fatalf("manifest downstream_patches = %d; want five reviewed squash-compatible contributions", len(manifest.DownstreamPatches))
	}
	downstream := manifest.DownstreamPatches[0]
	downstreamWants := []struct{ name, got, want string }{
		{"downstream.base_commit_sha", downstream.BaseCommit, "2fdb4319c69e9afd4f4d1b78b3f40da43d976ce0"},
		{"downstream.issue", downstream.Issue, "https://github.com/Project-Helianthus/helianthus-spine-go/issues/5"},
		{"downstream.pull_request", downstream.PullRequest, "https://github.com/Project-Helianthus/helianthus-spine-go/pull/6"},
	}
	for _, check := range downstreamWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	wantDownstreamFiles := []string{
		".github/workflows/default.yml",
		"contracttests/dependency_closure_remediation_test.go",
		"contracttests/dependency_closure_test.go",
		"contracttests/dependency_closure_verifier_test.go",
		"scripts/verify_dependency_closure.py",
		"spine/device_local.go",
		"spine/issue5_remote_removal_race_test.go",
	}
	if !reflect.DeepEqual(downstream.Files, wantDownstreamFiles) {
		t.Errorf("manifest downstream files = %v; want %v", downstream.Files, wantDownstreamFiles)
	}
	orderedEvents := manifest.DownstreamPatches[1]
	orderedEventWants := []struct{ name, got, want string }{
		{"ordered-events.base_commit_sha", orderedEvents.BaseCommit, "1e1e5546e42a26ba65c4e8596e95f4847ba396fe"},
		{"ordered-events.issue", orderedEvents.Issue, "https://github.com/Project-Helianthus/helianthus-spine-go/issues/7"},
		{"ordered-events.pull_request", orderedEvents.PullRequest, "https://github.com/Project-Helianthus/helianthus-spine-go/pull/8"},
	}
	for _, check := range orderedEventWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	wantOrderedEventFiles := []string{
		"contracttests/dependency_closure_remediation_test.go",
		"contracttests/dependency_closure_test.go",
		"contracttests/dependency_closure_verifier_test.go",
		"scripts/verify_dependency_closure.py",
		"spine/events.go",
		"spine/issue7_application_event_order_red_test.go",
	}
	if !reflect.DeepEqual(orderedEvents.Files, wantOrderedEventFiles) {
		t.Errorf("manifest ordered-event files = %v; want %v", orderedEvents.Files, wantOrderedEventFiles)
	}
	correlatedRoundTrip := manifest.DownstreamPatches[2]
	correlatedRoundTripWants := []struct{ name, got, want string }{
		{"correlated-round-trip.base_commit_sha", correlatedRoundTrip.BaseCommit, "7383c108f72309c3636d896948d7a8de6d001708"},
		{"correlated-round-trip.issue", correlatedRoundTrip.Issue, "https://github.com/Project-Helianthus/helianthus-spine-go/issues/9"},
		{"correlated-round-trip.pull_request", correlatedRoundTrip.PullRequest, "https://github.com/Project-Helianthus/helianthus-spine-go/pull/10"},
	}
	for _, check := range correlatedRoundTripWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	wantCorrelatedRoundTripFiles := []string{
		"api/roundtrip.go",
		"contracttests/dependency_closure_test.go",
		"spine/correlated_roundtrip_test.go",
		"spine/device_local.go",
		"spine/device_remote.go",
		"spine/roundtrip.go",
		"spine/send.go",
	}
	if !reflect.DeepEqual(correlatedRoundTrip.Files, wantCorrelatedRoundTripFiles) {
		t.Errorf("manifest correlated-round-trip files = %v; want %v", correlatedRoundTrip.Files, wantCorrelatedRoundTripFiles)
	}
	unknownFields := manifest.DownstreamPatches[3]
	unknownFieldWants := []struct{ name, got, want string }{
		{"unknown-fields.base_commit_sha", unknownFields.BaseCommit, "a35ec1c48a6cdd2cdcb9b6e56086360824fb21f2"},
		{"unknown-fields.issue", unknownFields.Issue, "https://github.com/Project-Helianthus/helianthus-spine-go/issues/11"},
		{"unknown-fields.pull_request", unknownFields.PullRequest, "https://github.com/Project-Helianthus/helianthus-spine-go/pull/12"},
	}
	for _, check := range unknownFieldWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	wantUnknownFieldFiles := []string{
		"api/issue11_unknown_fields_contract_red_test.go",
		"api/roundtrip.go",
		"contracttests/dependency_closure_test.go",
		"spine/correlated_unknown_fields.go",
		"spine/device_remote.go",
		"spine/issue11_unknown_fields_red_test.go",
		"spine/roundtrip.go",
	}
	if !reflect.DeepEqual(unknownFields.Files, wantUnknownFieldFiles) {
		t.Errorf("manifest unknown-field files = %v; want %v", unknownFields.Files, wantUnknownFieldFiles)
	}
	dispatchDisposition := manifest.DownstreamPatches[4]
	dispatchDispositionWants := []struct{ name, got, want string }{
		{"dispatch-disposition.base_commit_sha", dispatchDisposition.BaseCommit, "b21400335be90ea95a6cad5f512d1c8e22f2cdeb"},
		{"dispatch-disposition.issue", dispatchDisposition.Issue, "https://github.com/Project-Helianthus/helianthus-spine-go/issues/13"},
		{"dispatch-disposition.pull_request", dispatchDisposition.PullRequest, "https://github.com/Project-Helianthus/helianthus-spine-go/pull/14"},
	}
	for _, check := range dispatchDispositionWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	wantDispatchDispositionFiles := []string{
		"api/issue13_dispatch_disposition_contract_red_test.go",
		"api/roundtrip.go",
		"contracttests/dependency_closure_test.go",
		"spine/issue13_dispatch_disposition_red_test.go",
		"spine/roundtrip.go",
		"spine/send.go",
	}
	if !reflect.DeepEqual(dispatchDisposition.Files, wantDispatchDispositionFiles) {
		t.Errorf(
			"manifest dispatch-disposition files = %v; want %v",
			dispatchDisposition.Files,
			wantDispatchDispositionFiles,
		)
	}
	for name, record := range map[string]struct {
		content string
		patch   string
	}{
		"downstream":            {content: downstream.ContentSHA256, patch: downstream.PatchSHA256},
		"ordered-events":        {content: orderedEvents.ContentSHA256, patch: orderedEvents.PatchSHA256},
		"correlated-round-trip": {content: correlatedRoundTrip.ContentSHA256, patch: correlatedRoundTrip.PatchSHA256},
		"unknown-fields":        {content: unknownFields.ContentSHA256, patch: unknownFields.PatchSHA256},
		"dispatch-disposition":  {content: dispatchDisposition.ContentSHA256, patch: dispatchDisposition.PatchSHA256},
	} {
		for kind, digest := range map[string]string{"content": record.content, "patch": record.patch} {
			if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) {
				t.Errorf("manifest %s %s digest = %q; want SHA-256", name, kind, digest)
			}
		}
		if record.content == record.patch {
			t.Errorf("manifest %s content and patch digests must bind independent representations", name)
		}
	}
	patch := manifest.ReviewedPatches[0]
	patchWants := []struct{ name, got, want string }{
		{"patch.head_commit_sha", patch.HeadCommit, "8cfa9c8d49ba8f989ef889326249cb48797cd68a"},
		{"patch.merge_commit_sha", patch.MergeCommit, "f5aacd95d389c04f2455c121ed2e74ac95f19712"},
		{"patch.upstream_issue", patch.Issue, "https://github.com/enbility/spine-go/issues/38"},
		{"patch.upstream_pr", patch.PullRequest, "https://github.com/enbility/spine-go/pull/39"},
	}
	for _, check := range patchWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	if len(patch.Files) != 1 || patch.Files[0] != "spine/events.go" {
		t.Errorf("manifest patch files = %v; want [spine/events.go]", patch.Files)
	}
	ship := manifest.ReviewedDependencies[0]
	dependencyWants := []struct{ name, got, want string }{
		{"reviewed.module", ship.Module, canonicalShip},
		{"reviewed.version", ship.Version, canonicalVer},
		{"reviewed.tag_object_sha", ship.Tag, "a2a1cdb32c79fcbd3e659187d2f1ec017b8e7fa7"},
		{"reviewed.peeled_commit_sha", ship.Commit, "3d11169b8cb3e828cd24af066aac016c7edeb23d"},
		{"reviewed.tree_sha", ship.Tree, "1298a667e4d24c15027adf3775d99182cc174f24"},
		{"reviewed.license.path", ship.License.Path, "LICENSE"},
		{"reviewed.license.sha256", ship.License.SHA256, "c853996135802c50b3048937e48022bc00b41ff5f56a31cebe7d686bf91f87db"},
		{"reviewed.provenance_manifest.path", ship.Manifest.Path, "provenance/closure-manifest.json"},
		{"reviewed.provenance_manifest.sha256", ship.Manifest.SHA256, "54f91f18ab094825f68db61cad0423b4fadf2720179a09d2168d7cd988a43097"},
	}
	for _, check := range dependencyWants {
		if check.got != check.want {
			t.Errorf("manifest %s = %q; want %q", check.name, check.got, check.want)
		}
	}
	if len(manifest.DependencyControlInputs) == 0 {
		t.Error("manifest dependency_control_inputs must be explicit")
	}
}
func TestCommittedClosureVerifierIsExecutable(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "scripts", "verify_dependency_closure.py")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("required committed closure verifier %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("closure verifier is not a regular file: mode %s", info.Mode())
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("closure verifier is not executable: mode %s", info.Mode())
	}
}
func TestWorkflowSupportsReleaseBranchAndSARIF(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), ".github", "workflows", "default.yml")
	workflow := string(readFile(t, path))
	branch, permissions := workflowContract(workflow)
	if !branch {
		t.Error("workflow push branches do not include helianthus-v0.7")
	}
	want := map[string]string{"contents": "read", "security-events": "write"}
	if len(permissions) != len(want) {
		t.Errorf("top-level workflow permissions = %v; want only %v", permissions, want)
	}
	for name, value := range want {
		if permissions[name] != value {
			t.Errorf("workflow permission %s = %q; want %q", name, permissions[name], value)
		}
	}
	required := []string{
		"scripts/verify_dependency_closure.py",
		"gofmt -l",
		"GOWORK: \"off\"",
		"GOTOOLCHAIN: local",
		"GOFLAGS: -mod=readonly",
		"go list -m all",
		"go mod graph",
		"go list -deps ./...",
		"resolved-graph-closure.json",
		"git rev-parse HEAD",
		"fetch-depth: 0",
		"go version",
		"actions/upload-artifact",
	}
	for _, fragment := range required {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("workflow lacks required closure fragment %q", fragment)
		}
	}
	if strings.Index(workflow, "scripts/verify_dependency_closure.py") > strings.Index(workflow, "go list -m all") {
		t.Error("workflow runs resolved graph commands before tracked closure verifier")
	}
	if strings.Contains(workflow, "--issues-exit-code=0") || strings.Contains(workflow, "version: latest") || strings.Contains(workflow, "@master") {
		t.Error("workflow retains a lint bypass or mutable golangci selection")
	}
	uses := regexp.MustCompile(`(?m)^\s*uses:\s+[^\s]+@([0-9a-f]{40})\s+#\s+v\S+\s*$`).FindAllStringSubmatch(workflow, -1)
	if len(uses) != 7 {
		t.Errorf("workflow immutable action pins = %d; want 7 full commit pins with tag comments", len(uses))
	}
}
func TestProductionSourcesMatchUpstreamApartFromImportIdentity(t *testing.T) {
	root := repositoryRoot(t)
	h := sha256.New()
	for _, path := range trackedGoFiles(t, root) {
		if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, "contracttests/") {
			continue
		}
		src := normalizeCanonicalImports(t, path, readFile(t, filepath.Join(root, path)))
		src, err := format.Source(src)
		if err != nil {
			t.Fatalf("format normalized production source %s: %v", path, err)
		}
		fmt.Fprintf(h, "%s\x00", path)
		h.Write(src)
		h.Write([]byte{0})
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != productionHash {
		t.Errorf("normalized production source digest = %s; want reviewed upstream v0.7.0 plus patch digest %s", got, productionHash)
	}
}
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test source")
	}
	return filepath.Dir(filepath.Dir(file))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func trackedGoFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--", "*.go")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list tracked Go files: %v", err)
	}
	paths := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	sort.Strings(paths)
	return paths
}

func trackedGoSourceFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list tracked sources: %v", err)
	}
	var paths []string
	for _, path := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if regexp.MustCompile(`\.go(?:$|[._-])`).MatchString(filepath.Base(path)) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func hasImportPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func workflowContract(data string) (bool, map[string]string) {
	permissions := make(map[string]string)
	section := ""
	inPush := false
	inBranches := false
	branch := false
	for _, raw := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if trimmed == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent == 0 {
			section = strings.Trim(strings.TrimSuffix(trimmed, ":"), "\"'")
			inPush, inBranches = false, false
			continue
		}
		if section == "on" {
			if indent == 2 {
				inPush = strings.Trim(trimmed, "\"'") == "push:"
				inBranches = false
			} else if inPush && indent == 4 {
				inBranches = strings.Trim(trimmed, "\"'") == "branches:"
			} else if inBranches && indent >= 6 && strings.HasPrefix(trimmed, "-") {
				branch = branch || strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")), "\"'") == "helianthus-v0.7"
			}
		}
		if section == "permissions" && indent == 2 {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				permissions[strings.TrimSpace(parts[0])] = strings.Trim(strings.TrimSpace(parts[1]), "\"'")
			}
		}
	}
	return branch, permissions
}

func normalizeCanonicalImports(t *testing.T, path string, src []byte) []byte {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse production imports in %s: %v", path, err)
	}
	type replacement struct {
		start, end int
		value      string
	}
	var replacements []replacement
	for _, spec := range f.Imports {
		start := fset.Position(spec.Path.Pos()).Offset
		end := fset.Position(spec.Path.End()).Offset
		importPath, err := strconv.Unquote(string(src[start:end]))
		if err != nil {
			t.Fatalf("decode production import in %s: %v", path, err)
		}
		normalized := importPath
		if hasImportPrefix(importPath, canonicalModule) {
			normalized = upstreamSpine + strings.TrimPrefix(importPath, canonicalModule)
		} else if hasImportPrefix(importPath, canonicalShip) {
			normalized = upstreamShip + strings.TrimPrefix(importPath, canonicalShip)
		}
		if normalized != importPath {
			replacements = append(replacements, replacement{start, end, strconv.Quote(normalized)})
		}
	}
	out := append([]byte(nil), src...)
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		out = append(append(append([]byte(nil), out[:r.start]...), r.value...), out[r.end:]...)
	}
	return out
}
