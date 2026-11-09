package sdk_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The SDK moves to lanewayhq/plugins (spec #48), and Go forbids importing
// another module's internal packages, so it must build without Laneway's.
func TestTheSDKImportsNoneOfLanewaysInternalPackages(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "github.com/lanewayhq/laneway/internal/") {
			t.Errorf("the SDK imports %s", pkg)
		}
	}
}
