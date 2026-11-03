package bedrock_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCoreDependencyBoundary(t *testing.T) {
	const root = "github.com/kzs0/bedrock"
	run := func(args ...string) []byte {
		t.Helper()
		//nolint:staticcheck // Verify using this build's toolchain.
		cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		return out
	}
	modules := strings.Fields(string(run("list", "-m", "all")))
	if len(modules) != 1 || modules[0] != root {
		t.Errorf("core module graph contains dependencies: %v", modules)
	}
	decoder := json.NewDecoder(strings.NewReader(string(run("list", "-deps", "-json", "./..."))))
	for decoder.More() {
		var pkg struct {
			ImportPath string
			Standard   bool
		}
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatal(err)
		}
		if !pkg.Standard && pkg.ImportPath != root && !strings.HasPrefix(pkg.ImportPath, root+"/") {
			t.Errorf("production package imports external dependency %q", pkg.ImportPath)
		}
	}
	sum, err := os.ReadFile("go.sum")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(sum))) != 0 {
		t.Error("core go.sum must be absent or empty")
	}
}
