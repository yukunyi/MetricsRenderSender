package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
)

type FontCache struct {
	titleFont   font.Face
	contentFont font.Face
	smallFont   font.Face
	largeFont   font.Face
	headerFont  font.Face
	fontMap     map[int]font.Face
	fontPath    string
	mutex       sync.RWMutex
}

var fontLookupCache sync.Map

var fontAliasMap = map[string][]string{
	"microsoft yahei ui":  {"msyh.ttc", "msyh.ttf", "msyhbd.ttc", "msyhl.ttc"},
	"microsoft yahei":     {"msyh.ttc", "msyh.ttf", "msyhbd.ttc", "msyhl.ttc"},
	"segoe ui":            {"segoeui.ttf", "segoeuib.ttf", "segoeuii.ttf"},
	"simsun":              {"simsun.ttc", "simsun.ttf"},
	"consolas":            {"consola.ttf", "consolab.ttf"},
	"arial":               {"arial.ttf", "arial.ttf"},
	"noto sans cjk sc":    {"NotoSansCJK-Regular.ttc", "NotoSansCJKsc-Regular.otf"},
	"wenquanyi micro hei": {"wqy-microhei.ttc", "wqy-zenhei.ttc"},
	"sf mono":             {"SFNSMono.ttf"},
	"pingfang sc":         {"PingFang.ttc"},
}

func loadFontCache() (*FontCache, error) {
	return loadFontCacheForConfig(nil, GetGlobalCollectorConfig())
}

func loadFontCacheForConfig(current *FontCache, cfg *MonitorConfig) (*FontCache, error) {
	loadedFont := findSystemFontForConfig(cfg)
	if current != nil && current.fontPath == loadedFont {
		return current, nil
	}
	cache := &FontCache{
		fontMap: make(map[int]font.Face),
	}

	if loadedFont == "" {
		logWarnModule("font", "No suitable font found, using system default")
	} else {
		logInfoModule("font", "Using font: %s", filepath.Base(loadedFont))
	}
	cache.fontPath = loadedFont

	var err error
	cache.contentFont, err = loadFontFaceOrFallback(loadedFont, 16, nil)
	if err != nil {
		logWarnModule("font", "load content font failed, fallback to built-in: %v", err)
	}
	cache.titleFont, _ = loadFontFaceOrFallback(loadedFont, 18, cache.contentFont)
	cache.smallFont, _ = loadFontFaceOrFallback(loadedFont, 16, cache.contentFont)
	cache.largeFont, _ = loadFontFaceOrFallback(loadedFont, 18, cache.contentFont)
	cache.headerFont, _ = loadFontFaceOrFallback(loadedFont, 16, cache.contentFont)

	return cache, nil
}

func findSystemFont() string {
	return findSystemFontForConfig(GetGlobalCollectorConfig())
}

func findSystemFontForConfig(cfg *MonitorConfig) string {
	if cfg != nil {
		preferred := make([]string, 0, len(cfg.FontFamilies)+1)
		if strings.TrimSpace(cfg.GetDefaultFontName()) != "" {
			preferred = append(preferred, cfg.GetDefaultFontName())
		}
		for _, name := range cfg.FontFamilies {
			trimmed := strings.TrimSpace(name)
			if trimmed != "" {
				preferred = append(preferred, trimmed)
			}
		}
		if len(preferred) > 0 {
			for _, name := range preferred {
				if font := resolveFontCandidatePath(name); font != "" {
					return font
				}
			}
		}
	}

	fontFiles := []string{
		"wqy-microhei.ttc",
		"wqy-zenhei.ttc",
		"NotoSansCJK-Regular.ttc",
		"SourceHanSansSC-Regular.otf",
		"msyh.ttc",
		"simhei.ttf",
		"Roboto-Regular.ttf",
		"Ubuntu-Regular.ttf",
		"DejaVuSans.ttf",
		"LiberationSans-Regular.ttf",
		"arial.ttf",
		"Arial.ttf",
		"helvetica.ttf",
		"FreeSans.ttf",
	}

	for _, fontFile := range fontFiles {
		if font := resolveFontCandidatePath(fontFile); font != "" {
			return font
		}
	}

	return ""
}

func defaultFontDirs() []string {
	switch runtime.GOOS {
	case "windows":
		windir := strings.TrimSpace(os.Getenv("WINDIR"))
		dirs := []string{
			`C:\Windows\Fonts`,
		}
		if windir != "" {
			dirs = append([]string{filepath.Join(windir, "Fonts")}, dirs...)
		}
		return dirs
	case "darwin":
		return []string{
			"/System/Library/Fonts",
			"/Library/Fonts",
			"~/Library/Fonts",
		}
	default:
		return []string{
			"/usr/share/fonts",
			"/usr/local/share/fonts",
			"~/.fonts",
			"~/.local/share/fonts",
		}
	}
}

func expandHomePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if !strings.HasPrefix(path, "~") {
		return path
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return path
	}
	if path == "~" {
		return homeDir
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

func loadFontFaceOrFallback(fontPath string, size float64, fallback font.Face) (font.Face, error) {
	if strings.TrimSpace(fontPath) != "" {
		if face, err := gg.LoadFontFace(fontPath, size); err == nil && !isNilFontFace(face) {
			return face, nil
		} else if err != nil {
			if !isNilFontFace(fallback) {
				return fallback, err
			}
			return basicfont.Face7x13, err
		}
	}
	if !isNilFontFace(fallback) {
		return fallback, nil
	}
	return basicfont.Face7x13, fmt.Errorf("font path is empty")
}

func findFontByName(fontNames []string) string {
	candidates := make([]string, 0, len(fontNames))
	for _, name := range fontNames {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			candidates = append(candidates, strings.ToLower(trimmed))
		}
	}
	if len(candidates) == 0 {
		return ""
	}

	for _, fontName := range fontNames {
		expanded := expandHomePath(strings.TrimSpace(fontName))
		if expanded == "" {
			continue
		}
		info, err := os.Stat(expanded)
		if err != nil || info.IsDir() {
			continue
		}
		if face, err := gg.LoadFontFace(expanded, 16); err == nil && !isNilFontFace(face) {
			return expanded
		}
	}

	for _, dir := range defaultFontDirs() {
		expandedDir := expandHomePath(dir)
		if expandedDir == "" {
			continue
		}
		info, err := os.Stat(expandedDir)
		if err != nil || !info.IsDir() {
			continue
		}

		found := ""
		_ = filepath.WalkDir(expandedDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil || d.IsDir() {
				return nil
			}

			name := strings.ToLower(d.Name())
			if !strings.HasSuffix(name, ".ttf") && !strings.HasSuffix(name, ".ttc") && !strings.HasSuffix(name, ".otf") {
				return nil
			}
			for _, candidate := range candidates {
				if strings.Contains(name, candidate) {
					if _, err := gg.LoadFontFace(path, 16); err == nil {
						found = path
						return filepath.SkipAll
					}
					break
				}
			}
			return nil
		})
		if found != "" {
			return found
		}
	}

	return ""
}

func resolveFontCandidatePath(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return ""
	}
	cacheKey := strings.ToLower(name)
	if cached, ok := fontLookupCache.Load(cacheKey); ok {
		if path, okPath := cached.(string); okPath {
			return path
		}
	}
	candidates := []string{name}
	if aliases := resolveFontAliases(name); len(aliases) > 0 {
		candidates = append(candidates, aliases...)
	}
	resolved := findFontByName(candidates)
	fontLookupCache.Store(cacheKey, resolved)
	return resolved
}

func resolveFontAliases(name string) []string {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil
	}
	aliases, exists := fontAliasMap[key]
	if !exists || len(aliases) == 0 {
		return nil
	}
	items := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func isFontNameAvailable(raw string) bool {
	return resolveFontCandidatePath(raw) != ""
}

func sanitizeFontConfig(cfg *MonitorConfig) {
	if cfg == nil {
		return
	}
	families := make([]string, 0, len(cfg.FontFamilies))
	seen := make(map[string]struct{}, len(cfg.FontFamilies))
	for _, name := range cfg.FontFamilies {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if !isFontNameAvailable(trimmed) {
			continue
		}
		families = append(families, trimmed)
	}

	if strings.TrimSpace(cfg.DefaultFont) != "" && !isFontNameAvailable(cfg.DefaultFont) {
		logWarnModule("font", "configured default font not found, fallback to system defaults: %s", cfg.DefaultFont)
		cfg.DefaultFont = ""
	}

	if len(families) == 0 {
		for _, name := range getDefaultFontFamilies() {
			if isFontNameAvailable(name) {
				families = append(families, name)
			}
		}
	}
	if len(families) == 0 {
		for _, name := range getDefaultFontFamilies() {
			trimmed := strings.TrimSpace(name)
			if trimmed != "" {
				families = append(families, trimmed)
			}
		}
	}
	cfg.FontFamilies = families

	if strings.TrimSpace(cfg.DefaultFont) == "" {
		if len(cfg.FontFamilies) > 0 {
			cfg.DefaultFont = cfg.FontFamilies[0]
		} else {
			cfg.DefaultFont = ""
		}
	}
}

func isNilFontFace(face font.Face) bool {
	if face == nil {
		return true
	}
	value := reflect.ValueOf(face)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (fc *FontCache) GetFont(size int) (font.Face, error) {
	if fc == nil {
		return basicfont.Face7x13, fmt.Errorf("font cache is nil")
	}
	if size <= 0 {
		size = 16
	}

	fc.mutex.RLock()
	if fc.fontMap == nil {
		fc.mutex.RUnlock()
		fc.mutex.Lock()
		if fc.fontMap == nil {
			fc.fontMap = make(map[int]font.Face)
		}
		fc.mutex.Unlock()
		fc.mutex.RLock()
	}

	if face, exists := fc.fontMap[size]; exists && !isNilFontFace(face) {
		fc.mutex.RUnlock()
		return face, nil
	}
	fc.mutex.RUnlock()

	if strings.TrimSpace(fc.fontPath) != "" {
		face, err := gg.LoadFontFace(fc.fontPath, float64(size))
		if err == nil && !isNilFontFace(face) {
			fc.mutex.Lock()
			fc.fontMap[size] = face
			fc.mutex.Unlock()
			return face, nil
		}
		if !isNilFontFace(fc.contentFont) {
			return fc.contentFont, err
		}
		return basicfont.Face7x13, err
	}
	if !isNilFontFace(fc.contentFont) {
		return fc.contentFont, fmt.Errorf("font path is empty")
	}
	return basicfont.Face7x13, fmt.Errorf("font path is empty and no fallback font")
}
