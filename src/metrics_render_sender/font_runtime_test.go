package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
)

func writeRuntimeTestFonts(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular.ttf")
	mono := filepath.Join(dir, "mono.ttf")
	for path, data := range map[string][]byte{regular: goregular.TTF, mono: gomono.TTF} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return regular, mono
}

func TestFontCacheUsesExplicitConfigAndSelectedPath(t *testing.T) {
	initNormalizeOutputConfigTestDeps()
	regular, mono := writeRuntimeTestFonts(t)
	previous := GetGlobalCollectorConfig()
	SetGlobalCollectorConfig(&MonitorConfig{DefaultFont: mono})
	defer SetGlobalCollectorConfig(previous)

	cfg := &MonitorConfig{DefaultFont: regular, FontFamilies: []string{mono}}
	cache, err := loadFontCacheForConfig(nil, cfg)
	if err != nil || cache.fontPath != regular {
		t.Fatalf("explicit font selection = %v, %v", cache, err)
	}
	cfg.DefaultFont = filepath.Join(t.TempDir(), "missing.ttf")
	cfg.FontFamilies = []string{regular, mono}
	reused, err := loadFontCacheForConfig(cache, cfg)
	if err != nil || reused != cache {
		t.Fatalf("equivalent selected font did not reuse cache: %v", err)
	}
	cfg.FontFamilies = []string{mono, regular}
	changed, err := loadFontCacheForConfig(cache, cfg)
	if err != nil || changed == cache || changed.fontPath != mono {
		t.Fatalf("candidate order did not change selected font: %v, %v", changed, err)
	}
}

func TestWebRuntimeAppliesAndReusesConfiguredFonts(t *testing.T) {
	initNormalizeOutputConfigTestDeps()
	regular, mono := writeRuntimeTestFonts(t)
	previous := GetGlobalCollectorConfig()
	ResetGlobalCollectorManager()
	defer func() {
		ResetGlobalCollectorManager()
		SetGlobalCollectorConfig(previous)
	}()
	// A previous runtime's global configuration must not select this runtime's font.
	SetGlobalCollectorConfig(&MonitorConfig{DefaultFont: mono})
	cfg := &MonitorConfig{Width: 32, Height: 32, DefaultFont: regular, FontFamilies: []string{regular, mono}, Outputs: []OutputConfig{{Type: outputTypeMemImg}}}
	cfg.CollectorConfig = make(map[string]CollectorConfig)
	for _, name := range []string{collectorGoNativeCPU, collectorGoNativeMemory, collectorGoNativeSystem, collectorGoNativeDisk, collectorGoNativeNetwork, collectorCustomAll, collectorCoolerControl, collectorLibreHardwareMonitor, collectorRTSS} {
		setCollectorEnabled(cfg, name, false)
	}
	runtime, err := NewWebAPI(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	initial := runtime.fontCache
	if initial.fontPath != regular || runtime.renderManager.fontCache != initial {
		t.Fatal("initial runtime did not use configured font")
	}
	face, err := initial.GetFont(23)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Width = 40
	if err := runtime.ApplyPreviewConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.fontCache != initial {
		t.Fatal("ordinary preview edit rebuilt font cache")
	}
	if cached, err := runtime.fontCache.GetFont(23); err != nil || cached != face {
		t.Fatal("ordinary preview edit lost cached font face")
	}
	cfg.DefaultFont = mono
	if err := runtime.ApplyPreviewConfig(cfg); err != nil {
		t.Fatal(err)
	}
	preview := runtime.fontCache
	if preview == initial || preview.fontPath != mono || runtime.renderManager.fontCache != preview {
		t.Fatal("preview font change did not reach renderer")
	}
	previewFace, err := preview.GetFont(23)
	if err != nil {
		t.Fatal(err)
	}
	if font.MeasureString(previewFace, "iiii") == font.MeasureString(face, "iiii") {
		t.Fatal("font switch retained previous glyph metrics")
	}
	cfg.DefaultFont = regular
	if err := runtime.ApplyConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if runtime.fontCache == preview || runtime.fontCache.fontPath != regular || runtime.renderManager.fontCache != runtime.fontCache {
		t.Fatal("configuration switch did not update renderer font")
	}
}
