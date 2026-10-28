package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
)

func newCollectorAPIStore(t *testing.T) *ConfigStore {
	t.Helper()
	initNormalizeOutputConfigTestDeps()
	path := filepath.Join(t.TempDir(), "config.json")
	pm, err := NewProfileManager(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &MonitorConfig{CollectorConfig: make(map[string]CollectorConfig)}
	setCollectorEnabled(cfg, collectorGoNativeCPU, true)
	cfg, err = pm.Initialize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	registry := &CollectorManager{
		collectors:       map[string]Collector{collectorGoNativeCPU: nil},
		collectorEnabled: map[string]bool{collectorGoNativeCPU: true},
	}
	return &ConfigStore{path: path, cfg: cfg, profiles: pm, runtime: &WebAPI{registry: registry}}
}

func requestCollectorState(t *testing.T, store *ConfigStore, name string, enabled bool) int {
	t.Helper()
	e := echo.New()
	e.POST("/api/collectors/:name/state", collectorEnabledHandler(store, enabled))
	request := httptest.NewRequest(http.MethodPost, "/api/collectors/"+name+"/state", nil)
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	return response.Code
}

func TestCollectorAPIPersistsStateAcrossInitialization(t *testing.T) {
	store := newCollectorAPIStore(t)
	for _, enabled := range []bool{false, true} {
		if status := requestCollectorState(t, store, collectorGoNativeCPU, enabled); status != http.StatusOK {
			t.Fatalf("enabled=%v: status=%d", enabled, status)
		}
		if actual := store.collectorStates()[collectorGoNativeCPU]; actual != enabled {
			t.Fatalf("runtime state=%v, want %v", actual, enabled)
		}
		cfg, err := store.profiles.Initialize(&MonitorConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if actual := *cfg.CollectorConfig[collectorGoNativeCPU].Enabled; actual != enabled {
			t.Fatalf("restored state=%v, want %v", actual, enabled)
		}
	}
}

func TestCollectorAPIWriteFailurePreservesRunningState(t *testing.T) {
	for _, destination := range []string{"current", "profile"} {
		t.Run(destination, func(t *testing.T) {
			store := newCollectorAPIStore(t)
			path := store.path
			if destination == "profile" {
				path = store.profiles.profilePath(store.profiles.ActiveName())
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
			if status := requestCollectorState(t, store, collectorGoNativeCPU, false); status != http.StatusInternalServerError {
				t.Fatalf("write failure returned status=%d", status)
			}
			if !store.collectorStates()[collectorGoNativeCPU] {
				t.Fatal("failed save changed runtime state")
			}
			if !*store.getConfig().CollectorConfig[collectorGoNativeCPU].Enabled {
				t.Fatal("failed save changed store configuration")
			}
		})
	}
}

func TestCollectorAPIUnknownCollector(t *testing.T) {
	store := newCollectorAPIStore(t)
	if status := requestCollectorState(t, store, "unknown", false); status != http.StatusNotFound {
		t.Fatalf("unknown collector status=%d", status)
	}
	cfg, err := store.profiles.Initialize(&MonitorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.CollectorConfig["unknown"]; exists {
		t.Fatal("unknown collector was persisted")
	}
	if !store.collectorStates()[collectorGoNativeCPU] {
		t.Fatal("unknown collector request changed runtime state")
	}
}
