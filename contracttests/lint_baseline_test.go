package contracttests

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLintBaselineExemptionIsExactAndFailClosed(t *testing.T) {
	root := repositoryRoot(t)
	config := string(readFile(t, filepath.Join(root, ".golangci.yml")))
	workflow := string(readFile(t, filepath.Join(root, ".github", "workflows", "default.yml")))
	exactRule := `    # Mechanical fork baseline: exact upstream v0.7.0 expression at line 297.
    - path: ^model/commondatatypes_additions\.go$
      text: "G115: integer overflow conversion int -> int8"
      source: "^\\s*scaleValue = ScaleType\\(-numberOfDecimals\\)$"
      linters:
        - gosec
`
	if strings.Count(config, exactRule) != 1 {
		t.Fatalf("lint config must contain exactly one path, text, source, and linter-bound upstream G115 exemption")
	}
	for name, data := range map[string]string{"lint config": config, "workflow": workflow} {
		if strings.Contains(data, "--issues-exit-code=0") {
			t.Errorf("%s restores fail-open lint", name)
		}
	}
	if strings.Count(config, "G115") != 1 || strings.Count(config, "commondatatypes_additions\\.go") != 1 {
		t.Error("G115 exemption is duplicated or broader than the exact upstream file")
	}

	const expression = "\tscaleValue = ScaleType(-numberOfDecimals)"
	current := string(readFile(t, filepath.Join(root, "model", "commondatatypes_additions.go")))
	if strings.Count(current, expression) != 1 {
		t.Error("production source must contain the exact exempted upstream expression once")
	}
	// TestProductionSourcesMatchUpstreamApartFromImportIdentity independently
	// binds all normalized production source, including this expression, to productionHash.
}
