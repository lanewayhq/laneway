package registry_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lanewayhq/laneway/internal/registry"
	"github.com/lanewayhq/laneway/internal/service"
)

func TestAServiceRecordedWithAPortAbove65535GetsAFreshPortWhileOthersKeepTheirs(t *testing.T) {
	// Laneway once gave a version of an Engine with a high standard port,
	// such as rustfs's 9000, a port above 65535, which no Service can
	// listen on.
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "projects": [], "services": [
		{"engine": "rustfs", "version": "2", "port": 90002},
		{"engine": "redis", "version": "6", "port": 63706}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rustfs := service.Ref{Engine: "rustfs", Version: "2"}
	redis := service.Ref{Engine: "redis", Version: "6"}

	// So the daemon doesn't serve it at its port when it starts.
	if got, want := r.Services(), []registry.Service{{Ref: redis, Port: 63706}}; !slices.Equal(got, want) {
		t.Fatalf("Services with a port %+v, want %+v", got, want)
	}
	if svc, err := r.PlanService(rustfs, []int{59002, 59003}); err != nil || svc.Port != 59002 {
		t.Fatalf("planned rustfs:2 at %d (%v), want 59002", svc.Port, err)
	}
	if svc, err := r.AddService(rustfs, []int{59002, 59003}); err != nil || svc.Port != 59002 {
		t.Fatalf("added rustfs:2 at %d (%v), want 59002", svc.Port, err)
	}
	// Every other recorded port never changes, even when it isn't among
	// the candidates any more.
	if svc, err := r.AddService(redis, []int{63800}); err != nil || svc.Port != 63706 {
		t.Fatalf("redis:6 got %d (%v), want its recorded 63706", svc.Port, err)
	}

	reopened, err := registry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []registry.Service{{Ref: redis, Port: 63706}, {Ref: rustfs, Port: 59002}}
	if got := reopened.Services(); !slices.Equal(got, want) {
		t.Fatalf("recorded Services %+v, want %+v", got, want)
	}
}
