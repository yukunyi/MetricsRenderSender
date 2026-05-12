package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"metrics_render_sender/rtsssource"
	"metrics_render_sender/webui"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type WebServerOptions struct {
	Addr    string
	DevMode bool
	ViteURL string
}

type ConfigStore struct {
	path      string
	mu        sync.RWMutex
	cfg       *MonitorConfig
	runtime   *WebAPI
	profiles  *ProfileManager
	wsMu      sync.RWMutex
	wsClients map[*webSocketClient]struct{}
}

type WebMetaResponse struct {
	ConfigPath           string            `json:"config_path"`
	Platform             string            `json:"platform,omitempty"`
	Monitors             []string          `json:"monitors"`
	Collectors           []string          `json:"collectors,omitempty"`
	ItemTypes            []string          `json:"item_types"`
	StyleKeys            []StyleKeyMeta    `json:"style_keys,omitempty"`
	OutputTypes          []string          `json:"output_types"`
	FontFamilies         []string          `json:"font_families"`
	NetworkInterfaces    []string          `json:"network_interfaces"`
	CustomMonitorTypes   []string          `json:"custom_monitor_types"`
	CustomAggregateTypes []string          `json:"custom_aggregate_types"`
	MonitorAliasLabels   map[string]string `json:"monitor_alias_labels,omitempty"`
	ActiveProfile        string            `json:"active_profile,omitempty"`
}

type WebConfigResponse struct {
	Config *MonitorConfig `json:"config"`
}

var (
	runningWebServerMu sync.Mutex
	runningWebServer   *echo.Echo
	runningConfigMu    sync.RWMutex
	runningConfigStore *ConfigStore
)

func claimRunningWebServer(server *echo.Echo) error {
	if server == nil {
		return fmt.Errorf("web server instance is nil")
	}
	runningWebServerMu.Lock()
	defer runningWebServerMu.Unlock()
	if runningWebServer != nil {
		return fmt.Errorf("web server already running")
	}
	runningWebServer = server
	return nil
}

func releaseRunningWebServer(server *echo.Echo) {
	runningWebServerMu.Lock()
	defer runningWebServerMu.Unlock()
	if runningWebServer == server {
		runningWebServer = nil
	}
}

func claimRunningConfigStore(store *ConfigStore) {
	runningConfigMu.Lock()
	defer runningConfigMu.Unlock()
	runningConfigStore = store
}

func releaseRunningConfigStore(store *ConfigStore) {
	runningConfigMu.Lock()
	defer runningConfigMu.Unlock()
	if runningConfigStore == store {
		runningConfigStore = nil
	}
}

func UpdateRunningConfigStore(cfg *MonitorConfig) {
	if cfg == nil {
		return
	}
	runningConfigMu.RLock()
	store := runningConfigStore
	runningConfigMu.RUnlock()
	if store == nil {
		return
	}
	store.setConfig(cfg)
}

func stopRunningWebServer(timeout time.Duration) error {
	runningWebServerMu.Lock()
	server := runningWebServer
	runningWebServerMu.Unlock()
	if server == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type WebServerProcess struct {
	mu       sync.Mutex
	port     int
	host     string
	devMode  bool
	viteURL  string
	done     chan error
	stopping bool
}

func NewWebServerProcess(port int, devMode bool, viteURL string) *WebServerProcess {
	host, err := loadWebBindHost()
	if err != nil {
		logWarnModule("web", "load web bind host failed, fallback to %s: %v", defaultWebBindHost, err)
		host = defaultWebBindHost
	}
	return &WebServerProcess{
		port:    port,
		host:    host,
		devMode: devMode,
		viteURL: viteURL,
	}
}

func (p *WebServerProcess) URL() string {
	return buildWebAccessURL(p.port)
}

func (p *WebServerProcess) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done != nil
}

func (p *WebServerProcess) ListenHost() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return normalizeWebBindHost(p.host)
}

func (p *WebServerProcess) SetListenHost(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.host = normalizeWebBindHost(host)
}

func (p *WebServerProcess) Start() error {
	p.mu.Lock()
	if p.done != nil {
		p.mu.Unlock()
		return nil
	}
	done := make(chan error, 1)
	p.done = done
	p.stopping = false
	host := normalizeWebBindHost(p.host)
	p.mu.Unlock()

	options := WebServerOptions{
		Addr:    buildWebListenAddr(host, p.port),
		DevMode: p.devMode,
		ViteURL: p.viteURL,
	}

	go func() {
		err := RunWebServer(options)
		done <- err
		close(done)
	}()
	select {
	case err := <-done:
		p.mu.Lock()
		if p.done == done {
			p.done = nil
			p.stopping = false
		}
		p.mu.Unlock()
		if err == nil {
			return nil
		}
		return err
	case <-time.After(150 * time.Millisecond):
		go p.watchProcess(done)
		logInfoModule("tray", "web server started in-process pid=%d listen=%s open=%s", os.Getpid(), options.Addr, p.URL())
		return nil
	}
}

func (p *WebServerProcess) watchProcess(done chan error) {
	err, ok := <-done
	if !ok {
		err = nil
	}

	p.mu.Lock()
	stopping := p.stopping
	if p.done == done {
		p.done = nil
		p.stopping = false
	}
	p.mu.Unlock()

	if err != nil {
		if stopping {
			logInfoModule("tray", "web server stopped")
			return
		}
		logWarnModule("tray", "web server exited with error: %v", err)
		return
	}
	if stopping {
		logInfoModule("tray", "web server stopped")
		return
	}
	logInfoModule("tray", "web server exited")
}

func (p *WebServerProcess) Stop() error {
	p.mu.Lock()
	done := p.done
	if done != nil {
		p.stopping = true
	}
	p.mu.Unlock()

	if done == nil {
		return nil
	}

	if err := stopRunningWebServer(3 * time.Second); err != nil {
		return fmt.Errorf("stop web server failed: %w", err)
	}

	select {
	case err, ok := <-done:
		if !ok || err == nil {
			return nil
		}
		return err
	case <-time.After(4 * time.Second):
		return fmt.Errorf("wait web server stop timeout")
	}
}

func RunWebServer(options WebServerOptions) error {
	configPath, err := getUserConfigPath()
	if err != nil {
		return err
	}

	initialConfig, err := loadUserConfigOrDefault(configPath)
	if err != nil {
		return err
	}

	profileManager, initialConfig, err := InitializeGlobalProfileManager(configPath, initialConfig)
	if err != nil {
		return err
	}

	store := &ConfigStore{
		path:      configPath,
		cfg:       initialConfig,
		profiles:  profileManager,
		wsClients: make(map[*webSocketClient]struct{}),
	}
	runtime, err := AcquireSharedWebAPI(initialConfig)
	if err != nil {
		return err
	}
	defer ReleaseSharedWebAPI(runtime)
	store.runtime = runtime
	claimRunningConfigStore(store)
	defer releaseRunningConfigStore(store)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	e.GET("/api/meta", func(c echo.Context) error {
		store.touchRuntime()
		return c.JSON(http.StatusOK, buildWebMetaResponse(store))
	})

	e.GET("/api/coolercontrol/options", func(c echo.Context) error {
		store.touchRuntime()
		cfg := store.getRuntimeConfig()
		if cfg == nil {
			err := "runtime config unavailable"
			logWarnModule("web", "coolercontrol options request failed: %s", err)
			return c.JSON(http.StatusOK, map[string]interface{}{
				"items": []CoolerControlMonitorOption{},
				"error": err,
			})
		}
		options, err := listConfiguredCoolerControlOptions(cfg)
		if err != nil {
			url := cfg.GetCoolerControlURL()
			logWarnModule("web", "coolercontrol options request failed (url=%s): %v", url, err)
			return c.JSON(http.StatusOK, map[string]interface{}{
				"items": []CoolerControlMonitorOption{},
				"error": err.Error(),
			})
		}
		if options == nil {
			options = []CoolerControlMonitorOption{}
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"items": options})
	})

	e.GET("/api/librehardwaremonitor/options", func(c echo.Context) error {
		store.touchRuntime()
		cfg := store.getRuntimeConfig()
		if cfg == nil {
			err := "runtime config unavailable"
			logWarnModule("web", "librehardwaremonitor options request failed: %s", err)
			return c.JSON(http.StatusOK, map[string]interface{}{
				"items": []LibreHardwareMonitorMonitorOption{},
				"error": err,
			})
		}
		items, err := listConfiguredLibreHardwareMonitorOptions(cfg)
		if err != nil {
			url := cfg.GetLibreHardwareMonitorURL()
			logWarnModule("web", "librehardwaremonitor options request failed (url=%s): %v", url, err)
			return c.JSON(http.StatusOK, map[string]interface{}{
				"items": []LibreHardwareMonitorMonitorOption{},
				"error": err.Error(),
			})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"items": items})
	})

	e.GET("/api/config", func(c echo.Context) error {
		store.touchRuntime()
		return c.JSON(http.StatusOK, WebConfigResponse{Config: store.getConfig()})
	})

	e.POST("/api/preview/config", func(c echo.Context) error {
		var payload WebConfigResponse
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		}
		if payload.Config == nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing config"})
		}

		normalizeMonitorConfig(payload.Config)
		if err := store.applyPreviewConfigToRuntime(payload.Config); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"ok": true})
	})

	e.PUT("/api/config", func(c echo.Context) error {
		var payload WebConfigResponse
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		}
		if payload.Config == nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing config"})
		}

		normalizeMonitorConfig(payload.Config)
		if err := saveUserConfig(store.path, payload.Config); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		if active := store.profiles.ActiveName(); active != "" {
			if err := store.profiles.SaveProfile(active, payload.Config); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
		}
		store.setConfig(payload.Config)
		if err := store.applyConfigToRuntime(payload.Config); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":   true,
			"meta": buildWebMetaResponse(store),
		})
	})

	e.GET("/api/profiles", func(c echo.Context) error {
		store.touchRuntime()
		items, err := store.profiles.List()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"active": store.profiles.ActiveName(),
			"items":  items,
		})
	})

	e.POST("/api/profiles", func(c echo.Context) error {
		var payload struct {
			Name   string         `json:"name"`
			Config *MonitorConfig `json:"config"`
			Switch bool           `json:"switch"`
		}
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		}
		payload.Name = strings.TrimSpace(payload.Name)
		if payload.Name == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing profile name"})
		}
		cfg := payload.Config
		if cfg == nil {
			cfg = store.getConfig()
		}
		normalizeMonitorConfig(cfg)
		if err := store.profiles.SaveProfile(payload.Name, cfg); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if payload.Switch {
			if _, err := store.profiles.Switch(payload.Name); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			store.setConfig(cfg)
			if err := store.applyConfigToRuntime(cfg); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
		}
		items, _ := store.profiles.List()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":     true,
			"active": store.profiles.ActiveName(),
			"items":  items,
		})
	})

	e.POST("/api/profiles/rename", func(c echo.Context) error {
		var payload struct {
			OldName string `json:"old_name"`
			NewName string `json:"new_name"`
		}
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		}
		payload.OldName = strings.TrimSpace(payload.OldName)
		payload.NewName = strings.TrimSpace(payload.NewName)
		if payload.OldName == "" || payload.NewName == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing old_name or new_name"})
		}

		activeCfg, err := store.profiles.RenameProfile(payload.OldName, payload.NewName)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}

		if activeCfg != nil {
			store.setConfig(activeCfg)
			if err := store.applyConfigToRuntime(activeCfg); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
		}

		items, _ := store.profiles.List()
		resp := map[string]interface{}{
			"ok":     true,
			"active": store.profiles.ActiveName(),
			"items":  items,
		}
		if activeCfg != nil {
			resp["config"] = activeCfg
		}
		return c.JSON(http.StatusOK, resp)
	})

	e.GET("/api/profiles/:name", func(c echo.Context) error {
		store.touchRuntime()
		name := c.Param("name")
		cfg, err := store.profiles.LoadProfile(name)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"config": cfg})
	})

	e.PUT("/api/profiles/:name", func(c echo.Context) error {
		name := c.Param("name")
		var payload struct {
			Config *MonitorConfig `json:"config"`
		}
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		}
		if payload.Config == nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing config"})
		}
		normalizeMonitorConfig(payload.Config)
		if err := store.profiles.SaveProfile(name, payload.Config); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if store.profiles.ActiveName() == name {
			if err := saveUserConfig(store.path, payload.Config); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
			store.setConfig(payload.Config)
			if err := store.applyConfigToRuntime(payload.Config); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
		}
		items, _ := store.profiles.List()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":     true,
			"active": store.profiles.ActiveName(),
			"items":  items,
		})
	})

	e.POST("/api/profiles/switch", func(c echo.Context) error {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid payload: %v", err)})
		}
		cfg, err := store.profiles.Switch(payload.Name)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		store.setConfig(cfg)
		if err := store.applyConfigToRuntime(cfg); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		items, _ := store.profiles.List()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":     true,
			"active": store.profiles.ActiveName(),
			"items":  items,
			"config": cfg,
		})
	})

	e.DELETE("/api/profiles/:name", func(c echo.Context) error {
		name := c.Param("name")
		if err := store.profiles.DeleteProfile(name); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		currentCfg, err := loadUserConfigOrDefault(store.path)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		store.setConfig(currentCfg)
		if err := store.applyConfigToRuntime(currentCfg); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		items, _ := store.profiles.List()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"ok":     true,
			"active": store.profiles.ActiveName(),
			"items":  items,
		})
	})

	e.GET("/api/snapshot", func(c echo.Context) error {
		return c.JSON(http.StatusOK, store.snapshot())
	})

	e.GET("/api/ws", func(c echo.Context) error {
		return serveWebSocket(c, store)
	})

	e.GET("/api/runtime/monitor", func(c echo.Context) error {
		store.touchRuntime()
		stats := store.monitorStats()
		if stats == nil {
			return c.JSON(http.StatusOK, map[string]interface{}{"available": false})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"available": true,
			"stats":     stats,
		})
	})

	e.GET("/api/collectors", func(c echo.Context) error {
		store.touchRuntime()
		names := store.collectorNames()
		states := store.collectorStates()
		items := make([]map[string]interface{}, 0, len(names))
		for _, name := range names {
			items = append(items, map[string]interface{}{
				"name":    name,
				"enabled": states[name],
			})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"items": items,
		})
	})

	e.POST("/api/collectors/:name/enable", func(c echo.Context) error {
		name := strings.TrimSpace(c.Param("name"))
		if name == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing collector name"})
		}
		if !store.setCollectorEnabled(name, true) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "collector not found"})
		}
		cfg := store.getConfig()
		if cfg.CollectorConfig == nil {
			cfg.CollectorConfig = map[string]CollectorConfig{}
		}
		entry := cfg.CollectorConfig[name]
		entry.Enabled = boolPtr(true)
		if entry.Options == nil {
			entry.Options = map[string]interface{}{}
		}
		cfg.CollectorConfig[name] = entry
		store.setConfig(cfg)
		_ = saveUserConfig(store.path, cfg)
		return c.JSON(http.StatusOK, map[string]interface{}{"ok": true})
	})

	e.POST("/api/collectors/:name/disable", func(c echo.Context) error {
		name := strings.TrimSpace(c.Param("name"))
		if name == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "missing collector name"})
		}
		if !store.setCollectorEnabled(name, false) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "collector not found"})
		}
		cfg := store.getConfig()
		if cfg.CollectorConfig == nil {
			cfg.CollectorConfig = map[string]CollectorConfig{}
		}
		entry := cfg.CollectorConfig[name]
		entry.Enabled = boolPtr(false)
		if entry.Options == nil {
			entry.Options = map[string]interface{}{}
		}
		cfg.CollectorConfig[name] = entry
		store.setConfig(cfg)
		_ = saveUserConfig(store.path, cfg)
		return c.JSON(http.StatusOK, map[string]interface{}{"ok": true})
	})

	e.GET("/api/preview", func(c echo.Context) error {
		store.touchRuntime()
		if pngData, ok := GetMemImgPNG(); ok {
			return c.Blob(http.StatusOK, "image/png", pngData)
		}
		return c.NoContent(http.StatusNoContent)
	})

	if options.DevMode {
		viteURL := options.ViteURL
		if viteURL == "" {
			viteURL = "http://127.0.0.1:18087"
		}
		if err := webui.RegisterDevProxy(e, viteURL); err != nil {
			return err
		}
		logInfoModule("web", "Web config server started in dev mode: http://%s", options.Addr)
		logInfoModule("web", "Proxy target: %s", viteURL)
	} else {
		staticFS, err := getEmbeddedWebAssetsFS()
		if err != nil {
			return fmt.Errorf("failed to load embedded frontend: %w", err)
		}
		webui.RegisterEmbeddedFrontend(e, staticFS)
		logInfoModule("web", "Web config server started: http://%s", options.Addr)
	}

	if err := claimRunningWebServer(e); err != nil {
		return err
	}
	defer releaseRunningWebServer(e)
	err = e.Start(options.Addr)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func buildWebMetaResponse(store *ConfigStore) WebMetaResponse {
	config := store.getConfig()
	fontFamilies := collectFontFamilies(config)
	styleKeys := WebStyleKeyMeta()
	applyFontFamilyStyleKeyOptions(styleKeys, fontFamilies)
	return WebMetaResponse{
		ConfigPath:           store.path,
		Platform:             goruntime.GOOS,
		Monitors:             collectMonitorNames(config, store.runtime),
		Collectors:           store.collectorNames(),
		ItemTypes:            webItemTypes(),
		StyleKeys:            styleKeys,
		OutputTypes:          getSupportedOutputTypes(),
		FontFamilies:         fontFamilies,
		NetworkInterfaces:    listNetworkInterfaces(),
		CustomMonitorTypes:   []string{"file", "mixed", "coolercontrol", "librehardwaremonitor"},
		CustomAggregateTypes: []string{"max", "min", "avg", "sum"},
		MonitorAliasLabels:   monitorAliasLabels(),
		ActiveProfile:        store.profiles.ActiveName(),
	}
}

func applyFontFamilyStyleKeyOptions(styleKeys []StyleKeyMeta, fontFamilies []string) {
	if len(styleKeys) == 0 {
		return
	}
	options := make([]StyleOption, 0, len(fontFamilies))
	for _, family := range fontFamilies {
		trimmed := strings.TrimSpace(family)
		if trimmed == "" {
			continue
		}
		options = append(options, StyleOption{
			Label: trimmed,
			Value: trimmed,
		})
	}
	for idx := range styleKeys {
		if styleKeys[idx].Key != "font_family" {
			continue
		}
		styleKeys[idx].Options = options
		return
	}
}

func collectMonitorNames(config *MonitorConfig, runtimeState *WebAPI) []string {
	monitorSet := make(map[string]struct{})
	registryConfig := getCollectorManagerConfig()
	for _, monitor := range registryConfig.Items {
		monitorSet[monitor.Name] = struct{}{}
	}
	if goruntime.GOOS == "windows" && config != nil && config.IsRTSSCollectEnabled() {
		for _, option := range rtsssource.GetRTSSClient().ListMonitorOptions() {
			if strings.TrimSpace(option.Name) == "" {
				continue
			}
			monitorSet[option.Name] = struct{}{}
		}
	}
	if runtimeState != nil {
		for _, name := range runtimeState.AllMonitorNames() {
			monitorSet[name] = struct{}{}
		}
	}

	if config != nil {
		for _, item := range config.Items {
			for _, name := range collectItemMonitorRefs(&item) {
				if name == "" {
					continue
				}
				monitorSet[name] = struct{}{}
			}
		}
		for _, custom := range config.CustomMonitors {
			if strings.TrimSpace(custom.Name) != "" {
				monitorSet[custom.Name] = struct{}{}
			}
		}
	}

	monitors := make([]string, 0, len(monitorSet))
	for monitor := range monitorSet {
		monitors = append(monitors, monitor)
	}
	sort.Strings(monitors)
	return monitors
}

func listNetworkInterfaces() []string {
	options := []string{}
	interfaces, err := net.Interfaces()
	if err != nil {
		return options
	}
	for _, iface := range interfaces {
		if strings.TrimSpace(iface.Name) == "" {
			continue
		}
		options = append(options, iface.Name)
	}
	sort.Strings(options)
	return options
}

func collectFontFamilies(config *MonitorConfig) []string {
	appendUnique := func(items []string, seen map[string]struct{}, name string) []string {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return items
		}
		if _, exists := seen[trimmed]; exists {
			return items
		}
		seen[trimmed] = struct{}{}
		return append(items, trimmed)
	}

	items := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)

	systemDefault := strings.TrimSpace(getDefaultFontName())
	if systemDefault != "" {
		items = appendUnique(items, seen, systemDefault)
	}

	if config != nil {
		items = appendUnique(items, seen, config.GetDefaultFontName())
	}

	defaultCandidates := append([]string{}, getDefaultFontFamilies()...)
	sort.Strings(defaultCandidates)
	for _, name := range defaultCandidates {
		items = appendUnique(items, seen, name)
	}

	if config != nil {
		configCandidates := append([]string{}, config.FontFamilies...)
		sort.Strings(configCandidates)
		for _, name := range configCandidates {
			items = appendUnique(items, seen, name)
		}
	}

	return items
}

func getUserConfigPath() (string, error) {
	configDir, err := getUserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "config.json"), nil
}

func getUserConfigDir() (string, error) {
	if xdgDir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdgDir != "" {
		return filepath.Join(xdgDir, "metrics_render_sender"), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, ".config", "metrics_render_sender"), nil
}

func loadUserConfigOrDefault(path string) (*MonitorConfig, error) {
	if data, err := os.ReadFile(path); err == nil {
		var cfg MonitorConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid user config %s: %w", path, err)
		}
		normalizeMonitorConfig(&cfg)
		return &cfg, nil
	}

	cfg := &MonitorConfig{
		Name:                    "web",
		Width:                   480,
		Height:                  320,
		DefaultFont:             getDefaultFontName(),
		StyleBase:               map[string]interface{}{},
		FontFamilies:            getDefaultFontFamilies(),
		Outputs:                 getDefaultOutputConfigs(),
		OutputTypes:             getDefaultOutputTypes(),
		RefreshInterval:         1000,
		CollectWarnMS:           100,
		RenderWaitMaxMS:         300,
		HistorySize:             150,
		DefaultHistoryPoints:    150,
		NetworkInterface:        "",
		LibreHardwareMonitorURL: defaultLibreHardwareMonitorURL,
		CoolerControlURL:        defaultCoolerControlURL,
		CustomMonitors:          []CustomMonitorConfig{},
		CollectorConfig: map[string]CollectorConfig{
			collectorGoNativeCPU:          {Enabled: boolPtr(true), Options: map[string]interface{}{}},
			collectorGoNativeMemory:       {Enabled: boolPtr(true), Options: map[string]interface{}{}},
			collectorGoNativeSystem:       {Enabled: boolPtr(true), Options: map[string]interface{}{}},
			collectorGoNativeDisk:         {Enabled: boolPtr(true), Options: map[string]interface{}{}},
			collectorGoNativeNetwork:      {Enabled: boolPtr(true), Options: map[string]interface{}{}},
			collectorCustomAll:            {Enabled: boolPtr(true), Options: map[string]interface{}{}},
			collectorCoolerControl:        {Enabled: boolPtr(false), Options: map[string]interface{}{"url": defaultCoolerControlURL}},
			collectorLibreHardwareMonitor: {Enabled: boolPtr(false), Options: map[string]interface{}{"url": defaultLibreHardwareMonitorURL}},
			collectorRTSS:                 {Enabled: boolPtr(false), Options: map[string]interface{}{}},
		},
		Items: []ItemConfig{},
	}
	defaultMonitor := "go_native.cpu.temp"
	if goruntime.GOOS == "windows" {
		defaultMonitor = "go_native.cpu.usage"
	}
	cfg.Items = append(cfg.Items, ItemConfig{
		Type:    itemTypeSimpleValue,
		Monitor: defaultMonitor,
		Unit:    "auto",
		X:       12,
		Y:       12,
		Width:   180,
		Height:  56,
	})
	normalizeMonitorConfig(cfg)
	return cfg, nil
}

func saveUserConfig(path string, cfg *MonitorConfig) error {
	if cfg != nil {
		normalizeMonitorConfig(cfg)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	return nil
}

func normalizeMonitorConfig(cfg *MonitorConfig) {
	if cfg.CollectorConfig == nil {
		cfg.CollectorConfig = make(map[string]CollectorConfig)
	}
	ensureCollectorConfigDefault(cfg, collectorGoNativeCPU, true)
	ensureCollectorConfigDefault(cfg, collectorGoNativeMemory, true)
	ensureCollectorConfigDefault(cfg, collectorGoNativeSystem, true)
	ensureCollectorConfigDefault(cfg, collectorGoNativeDisk, true)
	ensureCollectorConfigDefault(cfg, collectorGoNativeNetwork, true)
	ensureCollectorConfigDefault(cfg, collectorCustomAll, true)
	ensureCollectorConfigDefault(cfg, collectorCoolerControl, false)
	ensureCollectorConfigDefault(cfg, collectorLibreHardwareMonitor, false)
	defaultRTSS := defaultRTSSCollectorEnabledForPlatform(goruntime.GOOS)
	ensureCollectorConfigDefault(cfg, collectorRTSS, defaultRTSS)
	if goruntime.GOOS == "windows" && configNeedsRTSS(cfg) {
		setCollectorEnabled(cfg, collectorRTSS, true)
	}
	if cfg.EnableRTSSCollect {
		entry := cfg.CollectorConfig[collectorRTSS]
		enabled := true
		entry.Enabled = &enabled
		if entry.Options == nil {
			entry.Options = map[string]interface{}{}
		}
		cfg.CollectorConfig[collectorRTSS] = entry
	}
	if cfg.IsCollectorEnabled(collectorRTSS, false) {
		cfg.EnableRTSSCollect = true
	}
	enforceCollectorPlatformSupport(cfg)

	if cfg.FontFamilies == nil {
		cfg.FontFamilies = getDefaultFontFamilies()
	}
	if goruntime.GOOS == "windows" {
		if strings.Contains(strings.ToLower(strings.TrimSpace(cfg.DefaultFont)), "wqy") {
			cfg.DefaultFont = getDefaultFontName()
		}
	}
	if cfg.CustomMonitors == nil {
		cfg.CustomMonitors = []CustomMonitorConfig{}
	}
	if cfg.Items == nil {
		cfg.Items = []ItemConfig{}
	}
	if cfg.Width <= 0 {
		cfg.Width = 480
	}
	if cfg.Height <= 0 {
		cfg.Height = 320
	}
	minEdge := cfg.Width
	if cfg.Height < minEdge {
		minEdge = cfg.Height
	}
	maxLayoutPadding := (minEdge - 10) / 2
	if maxLayoutPadding < 0 {
		maxLayoutPadding = 0
	}
	if cfg.LayoutPadding < 0 {
		cfg.LayoutPadding = 0
	}
	if cfg.LayoutPadding > maxLayoutPadding {
		cfg.LayoutPadding = maxLayoutPadding
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 1000
	}
	if cfg.RefreshInterval < 100 {
		cfg.RefreshInterval = 100
	}
	if cfg.RefreshInterval > 10000 {
		cfg.RefreshInterval = 10000
	}
	if cfg.CollectWarnMS <= 0 {
		cfg.CollectWarnMS = 100
	}
	if cfg.CollectWarnMS < 10 {
		cfg.CollectWarnMS = 10
	}
	if cfg.CollectWarnMS > 10000 {
		cfg.CollectWarnMS = 10000
	}
	if cfg.RenderWaitMaxMS <= 0 {
		cfg.RenderWaitMaxMS = 300
	}
	if cfg.RenderWaitMaxMS < 0 {
		cfg.RenderWaitMaxMS = 0
	}
	if cfg.RenderWaitMaxMS > cfg.RefreshInterval {
		cfg.RenderWaitMaxMS = cfg.RefreshInterval
	}
	if cfg.MonitorUpdateWorkers < 0 {
		cfg.MonitorUpdateWorkers = 0
	}
	if cfg.MonitorUpdateQueueSize < 0 {
		cfg.MonitorUpdateQueueSize = 0
	}
	if strings.TrimSpace(cfg.DefaultFont) == "" {
		cfg.DefaultFont = getDefaultFontName()
	}
	sanitizeFontConfig(cfg)
	if cfg.Outputs == nil {
		if len(cfg.OutputTypes) > 0 {
			cfg.Outputs = outputConfigsFromTypes(cfg.OutputTypes)
		}
	}
	cfg.Outputs = normalizeOutputConfigs(cfg.Outputs)
	cfg.OutputTypes = outputEnabledTypeNames(cfg.Outputs)
	cfg.NetworkInterface = strings.TrimSpace(cfg.NetworkInterface)
	if strings.EqualFold(cfg.NetworkInterface, "auto") {
		cfg.NetworkInterface = ""
	}
	if strings.TrimSpace(cfg.CoolerControlURL) == "" {
		cfg.CoolerControlURL = defaultCoolerControlURL
	}
	if strings.TrimSpace(cfg.LibreHardwareMonitorURL) == "" {
		cfg.LibreHardwareMonitorURL = defaultLibreHardwareMonitorURL
	}
	if cfg.DefaultHistoryPoints <= 0 {
		if cfg.HistorySize > 0 {
			cfg.DefaultHistoryPoints = cfg.HistorySize
		} else {
			cfg.DefaultHistoryPoints = 150
		}
	}
	if cfg.DefaultHistoryPoints < 10 {
		cfg.DefaultHistoryPoints = 10
	}
	if cfg.DefaultHistoryPoints > 5000 {
		cfg.DefaultHistoryPoints = 5000
	}
	if cfg.HistorySize <= 0 {
		cfg.HistorySize = cfg.DefaultHistoryPoints
	}
	ensureTypeDefaults(cfg)
	cfg.ThresholdGroups = normalizeThresholdGroups(cfg.ThresholdGroups)
	normalizeStyleConfiguration(cfg)
	setCollectorOptionDefault(cfg, collectorCoolerControl, "url", defaultCoolerControlURL)
	setCollectorOptionDefault(cfg, collectorLibreHardwareMonitor, "url", defaultLibreHardwareMonitorURL)
	removeCollectorOption(cfg, collectorCoolerControl, "username")

	usedItemIDs := make(map[string]struct{}, len(cfg.Items))
	normalizedItems := make([]ItemConfig, 0, len(cfg.Items))
	for idx := range cfg.Items {
		item := &cfg.Items[idx]
		rawType := strings.TrimSpace(item.Type)
		itemType := normalizeItemType(rawType)
		if itemType == "" {
			logWarnModule("config", "skip item idx=%d invalid type=%q", idx, rawType)
			continue
		}
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			item.ID = generateItemID(idx)
		}
		if _, exists := usedItemIDs[item.ID]; exists {
			item.ID = generateItemID(idx)
		}
		usedItemIDs[item.ID] = struct{}{}
		item.Type = itemType
		item.Monitor = normalizeMonitorAlias(item.Monitor)
		item.EditUIName = defaultEditUIName(item.EditUIName, idx, item)
		if !cfg.AllowCustomStyle {
			item.CustomStyle = false
		}
		if item.Width <= 0 {
			item.Width = 120
		}
		if item.Height <= 0 {
			item.Height = 40
		}
		if item.Type == itemTypeFullTable {
			item.Unit = ""
			item.MinValue = nil
			item.MaxValue = nil
			normalizeFullTableItemAttrs(item)
		} else if isCollectorItemType(item.Type) {
			if strings.TrimSpace(item.Unit) == "" {
				item.Unit = "auto"
			}
			if !isRangeItemType(item.Type) {
				item.MinValue = nil
				item.MaxValue = nil
			}
		} else {
			item.Unit = ""
			item.MinValue = nil
			item.MaxValue = nil
			if item.RenderAttrsMap != nil {
				delete(item.RenderAttrsMap, "rows")
				delete(item.RenderAttrsMap, "columns")
				delete(item.RenderAttrsMap, "column_count")
				delete(item.RenderAttrsMap, "col_count")
				delete(item.RenderAttrsMap, "row_count")
			}
		}
		normalizeItemStyleConfiguration(cfg, item)
		prepareRenderItemRuntime(cfg, item)
		normalizedItems = append(normalizedItems, *item)
	}
	cfg.Items = normalizedItems

	for idx := range cfg.CustomMonitors {
		custom := &cfg.CustomMonitors[idx]
		custom.Source = normalizeMonitorAlias(custom.Source)
		if len(custom.Sources) == 0 {
			continue
		}
		normalized := make([]string, 0, len(custom.Sources))
		for _, source := range custom.Sources {
			name := normalizeMonitorAlias(source)
			if name == "" {
				continue
			}
			normalized = append(normalized, name)
		}
		custom.Sources = normalized
	}
}

func ensureTypeDefaults(cfg *MonitorConfig) {
	if cfg == nil {
		return
	}
	if cfg.TypeDefaults == nil {
		cfg.TypeDefaults = map[string]ItemTypeDefaults{}
	}
	for _, itemType := range webItemTypes() {
		entry := cfg.TypeDefaults[itemType]
		entry = normalizeTypeDefaultsEntry(cfg, itemType, entry)
		cfg.TypeDefaults[itemType] = entry
	}
}

func normalizeTypeDefaultsEntry(_ *MonitorConfig, itemType string, entry ItemTypeDefaults) ItemTypeDefaults {
	entry.Style = normalizeStyleMap(entry.Style, styleScopeType, itemType)
	entry.RenderAttrsMap = stripStyleKeysFromRenderAttrs(entry.RenderAttrsMap)
	return entry
}

func enforceCollectorPlatformSupport(cfg *MonitorConfig) {
	if cfg == nil || cfg.CollectorConfig == nil {
		return
	}
	for name, entry := range cfg.CollectorConfig {
		if isCollectorSupportedOnCurrentPlatform(name) {
			continue
		}
		disabled := false
		entry.Enabled = &disabled
		if entry.Options == nil {
			entry.Options = map[string]interface{}{}
		}
		cfg.CollectorConfig[name] = entry
	}
}

func boolPtr(value bool) *bool {
	v := value
	return &v
}

func setCollectorEnabled(cfg *MonitorConfig, name string, enabled bool) {
	if cfg == nil {
		return
	}
	entry := cfg.CollectorConfig[name]
	entry.Enabled = boolPtr(enabled)
	if entry.Options == nil {
		entry.Options = map[string]interface{}{}
	}
	cfg.CollectorConfig[name] = entry
}

func configNeedsRTSS(cfg *MonitorConfig) bool {
	if cfg == nil {
		return false
	}
	for _, item := range cfg.Items {
		for _, name := range collectItemMonitorRefs(&item) {
			if isRTSSMonitorRef(name) {
				return true
			}
		}
	}
	for _, custom := range cfg.CustomMonitors {
		name := strings.ToLower(strings.TrimSpace(custom.Name))
		if isRTSSMonitorRef(name) {
			return true
		}
		for _, source := range custom.Sources {
			if isRTSSMonitorRef(source) {
				return true
			}
		}
	}
	return false
}

func ensureCollectorConfigDefault(cfg *MonitorConfig, name string, defaultEnabled bool) {
	if cfg == nil {
		return
	}
	current, exists := cfg.CollectorConfig[name]
	if !exists {
		enabled := defaultEnabled
		cfg.CollectorConfig[name] = CollectorConfig{
			Enabled: &enabled,
			Options: map[string]interface{}{},
		}
		return
	}
	if current.Enabled == nil {
		enabled := defaultEnabled
		current.Enabled = &enabled
	}
	if current.Options == nil {
		current.Options = map[string]interface{}{}
	}
	cfg.CollectorConfig[name] = current
}

func setCollectorOptionDefault(cfg *MonitorConfig, collectorName, optionKey, value string) {
	if cfg == nil || strings.TrimSpace(value) == "" {
		return
	}
	entry, exists := cfg.CollectorConfig[collectorName]
	if !exists {
		enabled := false
		entry = CollectorConfig{
			Enabled: &enabled,
			Options: map[string]interface{}{},
		}
	}
	if entry.Options == nil {
		entry.Options = map[string]interface{}{}
	}
	current := strings.TrimSpace(fmt.Sprintf("%v", entry.Options[optionKey]))
	if current == "" {
		entry.Options[optionKey] = value
	}
	cfg.CollectorConfig[collectorName] = entry
}

func removeCollectorOption(cfg *MonitorConfig, collectorName, optionKey string) {
	if cfg == nil {
		return
	}
	entry, exists := cfg.CollectorConfig[collectorName]
	if !exists || entry.Options == nil {
		return
	}
	delete(entry.Options, optionKey)
	cfg.CollectorConfig[collectorName] = entry
}

func defaultEditUIName(current string, idx int, item *ItemConfig) string {
	_ = idx
	_ = item
	if strings.TrimSpace(current) != "" {
		return strings.TrimSpace(current)
	}
	return ""
}

func generateItemID(idx int) string {
	return fmt.Sprintf("itm_%d_%d", time.Now().UnixNano(), idx)
}

func normalizeItemType(itemType string) string {
	trimmed := strings.ToLower(strings.TrimSpace(itemType))
	if trimmed == "" {
		return ""
	}
	if _, ok := allItemTypeSet[trimmed]; ok {
		return trimmed
	}
	return ""
}

func normalizeLevelColors(colors []string, fallback []string) []string {
	result := make([]string, 0, 4)
	for _, color := range colors {
		trimmed := strings.TrimSpace(color)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
		if len(result) == 4 {
			break
		}
	}
	if len(result) == 0 && len(fallback) > 0 {
		for _, color := range fallback {
			trimmed := strings.TrimSpace(color)
			if trimmed != "" {
				result = append(result, trimmed)
			}
			if len(result) == 4 {
				break
			}
		}
	}
	return result
}

func cloneMonitorConfig(cfg *MonitorConfig) *MonitorConfig {
	data, _ := json.Marshal(cfg)
	var copyCfg MonitorConfig
	_ = json.Unmarshal(data, &copyCfg)
	return &copyCfg
}

func (s *ConfigStore) getConfig() *MonitorConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMonitorConfig(s.cfg)
}

func (s *ConfigStore) setConfig(cfg *MonitorConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cloneMonitorConfig(cfg)
}

func (s *ConfigStore) touchRuntime() {
	if s.runtime != nil {
		s.runtime.Touch()
	}
}

func (s *ConfigStore) applyConfigToRuntime(cfg *MonitorConfig) error {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.ApplyConfig(cfg)
}

func (s *ConfigStore) applyPreviewConfigToRuntime(cfg *MonitorConfig) error {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.ApplyPreviewConfig(cfg)
}

func (s *ConfigStore) getRuntimeConfig() *MonitorConfig {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.CurrentConfig()
}

func (s *ConfigStore) snapshot() WebSnapshotResponse {
	if s.runtime == nil {
		return WebSnapshotResponse{
			Mode:      "required",
			UpdatedAt: time.Now().Format(time.RFC3339Nano),
			Monitors:  []string{},
			Values:    map[string]WebMonitorSnapshotItem{},
		}
	}
	return s.runtime.Snapshot()
}

func (s *ConfigStore) monitorStats() *CollectorManagerStats {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.MonitorStats()
}

func (s *ConfigStore) collectorStates() map[string]bool {
	if s.runtime == nil {
		return map[string]bool{}
	}
	return s.runtime.CollectorStates()
}

func (s *ConfigStore) collectorNames() []string {
	if s.runtime == nil {
		return []string{}
	}
	return s.runtime.CollectorNames()
}

func (s *ConfigStore) setCollectorEnabled(name string, enabled bool) bool {
	if s.runtime == nil {
		return false
	}
	return s.runtime.SetCollectorEnabled(name, enabled)
}
