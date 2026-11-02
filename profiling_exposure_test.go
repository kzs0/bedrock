package bedrock_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCoreImportDoesNotExposeGlobalProfiles(t *testing.T) {
	//nolint:staticcheck // Verify using this build's toolchain.
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./testdata/importcheck")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("import check: %v\n%s", err, output)
	}
	var statuses map[string]int
	if err := json.Unmarshal(output, &statuses); err != nil {
		t.Fatalf("decode status: %v\n%s", err, output)
	}
	for path, status := range statuses {
		if status != 404 {
			t.Errorf("core import exposes %s with status %d", path, status)
		}
	}
}
