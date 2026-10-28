package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

const (
	wsWriteWait      = 5 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = 25 * time.Second
	wsPushInterval   = 250 * time.Millisecond
	wsSendBufferSize = 16
)

var errWSSendQueueFull = errors.New("send queue full")

var webSocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type wsClientRequest struct {
	Type    string         `json:"type"`
	ID      string         `json:"id,omitempty"`
	Profile string         `json:"profile,omitempty"`
	Config  *MonitorConfig `json:"config,omitempty"`
}

type wsServerResponse struct {
	Type   string      `json:"type"`
	ID     string      `json:"id,omitempty"`
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
	Result interface{} `json:"result,omitempty"`
}

type wsRuntimeMessage struct {
	Type       string               `json:"type"`
	Snapshot   *WebSnapshotResponse `json:"snapshot,omitempty"`
	PreviewPNG string               `json:"preview_png,omitempty"`
}

type webSocketClient struct {
	conn   *websocket.Conn
	store  *ConfigStore
	sendCh chan []byte
	closed chan struct{}
	done   chan struct{}
	loops  sync.WaitGroup
	once   sync.Once
}

func serveWebSocket(c echo.Context, store *ConfigStore) error {
	conn, err := webSocketUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	client := &webSocketClient{
		conn:   conn,
		store:  store,
		sendCh: make(chan []byte, wsSendBufferSize),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}

	defer close(client.done)
	if !store.registerWebSocketClient(client) {
		client.close()
		return nil
	}
	defer func() {
		client.close()
		client.loops.Wait()
		store.unregisterWebSocketClient(client)
	}()
	client.loops.Add(2)

	go client.writeLoop()
	go client.pushLoop()
	return client.readLoop()
}

func (s *ConfigStore) registerWebSocketClient(client *webSocketClient) bool {
	s.wsMu.Lock()
	if s.wsClosed {
		s.wsMu.Unlock()
		return false
	}
	s.wsClients[client] = struct{}{}
	count := len(s.wsClients)
	s.wsMu.Unlock()

	if s.runtime != nil {
		s.runtime.SetRealtimeConnectionCount(count)
	}
	return true
}

func (s *ConfigStore) closeWebSocketClients() {
	s.wsMu.Lock()
	s.wsClosed = true
	clients := make([]*webSocketClient, 0, len(s.wsClients))
	for client := range s.wsClients {
		clients = append(clients, client)
	}
	s.wsMu.Unlock()

	for _, client := range clients {
		client.close()
	}
	for _, client := range clients {
		<-client.done
	}
}

func (s *ConfigStore) unregisterWebSocketClient(client *webSocketClient) {
	s.wsMu.Lock()
	delete(s.wsClients, client)
	count := len(s.wsClients)
	s.wsMu.Unlock()

	if s.runtime != nil {
		s.runtime.SetRealtimeConnectionCount(count)
	}
}

func (c *webSocketClient) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

func (c *webSocketClient) writeLoop() {
	defer c.loops.Done()
	defer c.close()
	pingTicker := time.NewTicker(wsPingPeriod)
	defer pingTicker.Stop()

	for {
		select {
		case <-c.closed:
			return
		case message, ok := <-c.sendCh:
			if !ok {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-pingTicker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *webSocketClient) pushLoop() {
	defer c.loops.Done()
	defer c.close()
	ticker := time.NewTicker(wsPushInterval)
	defer ticker.Stop()
	lastUpdatedAt := ""
	lastPreviewLen := 0
	var lastPreviewCRC uint32

	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			snapshot := c.store.snapshot()
			pngData, _ := GetMemImgPNG()
			previewLen := len(pngData)
			var previewCRC uint32
			if previewLen > 0 {
				previewCRC = crc32.ChecksumIEEE(pngData)
			}
			if snapshot.UpdatedAt == lastUpdatedAt && previewLen == lastPreviewLen && previewCRC == lastPreviewCRC {
				continue
			}
			if err := c.sendRuntimeWithPreview(&snapshot, pngData); err != nil {
				if errors.Is(err, errWSSendQueueFull) {
					// Keep connection alive; skip this push and wait for next update.
					continue
				}
				return
			}
			lastUpdatedAt = snapshot.UpdatedAt
			lastPreviewLen = previewLen
			lastPreviewCRC = previewCRC
		}
	}
}

func (c *webSocketClient) readLoop() error {
	c.conn.SetReadLimit(4 * 1024 * 1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	if err := c.sendRuntimeSnapshotNow(); err != nil {
		return nil
	}

	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return nil
		}

		var req wsClientRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			_ = c.sendError(req.ID, "invalid message")
			continue
		}
		if strings.TrimSpace(req.Type) == "" {
			_ = c.sendError(req.ID, "missing type")
			continue
		}

		if err := c.handleRequest(req); err != nil {
			_ = c.sendError(req.ID, err.Error())
		}
	}
}

func (c *webSocketClient) sendJSON(data interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	select {
	case <-c.closed:
		return errors.New("closed")
	case c.sendCh <- payload:
		return nil
	default:
		return errWSSendQueueFull
	}
}

func (c *webSocketClient) sendRuntime(snapshot *WebSnapshotResponse) error {
	pngData, _ := GetMemImgPNG()
	return c.sendRuntimeWithPreview(snapshot, pngData)
}

func (c *webSocketClient) sendRuntimeWithPreview(snapshot *WebSnapshotResponse, pngData []byte) error {
	msg := wsRuntimeMessage{
		Type:     "runtime",
		Snapshot: snapshot,
	}
	if len(pngData) > 0 {
		msg.PreviewPNG = base64.StdEncoding.EncodeToString(pngData)
	}
	return c.sendJSON(msg)
}

func (c *webSocketClient) sendRuntimeSnapshotNow() error {
	snapshot := c.store.snapshot()
	return c.sendRuntime(&snapshot)
}

func (c *webSocketClient) sendResponse(id string, result interface{}) error {
	return c.sendJSON(wsServerResponse{
		Type:   "response",
		ID:     id,
		OK:     true,
		Result: result,
	})
}

func (c *webSocketClient) sendError(id string, message string) error {
	return c.sendJSON(wsServerResponse{
		Type:  "response",
		ID:    id,
		OK:    false,
		Error: message,
	})
}

func (c *webSocketClient) handleRequest(req wsClientRequest) error {
	switch strings.TrimSpace(req.Type) {
	case "ping":
		return c.sendResponse(req.ID, map[string]interface{}{"pong": true})
	case "request_runtime":
		if err := c.sendRuntimeSnapshotNow(); err != nil {
			return err
		}
		return c.sendResponse(req.ID, map[string]interface{}{"ok": true})
	case "preview_config":
		if req.Config == nil {
			return fmt.Errorf("missing config")
		}
		normalizeMonitorConfig(req.Config)
		if err := c.store.applyPreviewConfigToRuntime(req.Config); err != nil {
			return err
		}
		if err := c.sendRuntimeSnapshotNow(); err != nil {
			return err
		}
		return c.sendResponse(req.ID, map[string]interface{}{"ok": true})
	case "save_profile_config":
		if req.Config == nil {
			return fmt.Errorf("missing config")
		}
		result, err := c.store.saveProfileConfigRealtime(req.Profile, req.Config)
		if err != nil {
			return err
		}
		if err := c.sendRuntimeSnapshotNow(); err != nil {
			return err
		}
		return c.sendResponse(req.ID, result)
	default:
		return fmt.Errorf("unknown message type: %s", req.Type)
	}
}

func (s *ConfigStore) saveProfileConfigRealtime(profile string, cfg *MonitorConfig) (map[string]interface{}, error) {
	profileName := strings.TrimSpace(profile)
	if profileName == "" {
		profileName = s.profiles.ActiveName()
	}
	if profileName == "" {
		profileName = "default"
	}

	configCopy := cloneMonitorConfig(cfg)
	normalizeMonitorConfig(configCopy)

	if err := s.profiles.SaveProfile(profileName, configCopy); err != nil {
		return nil, err
	}
	if s.profiles.ActiveName() == profileName {
		if err := saveUserConfig(s.path, configCopy); err != nil {
			return nil, err
		}
		s.setConfig(configCopy)
		if err := s.applyConfigToRuntime(configCopy); err != nil {
			return nil, err
		}
	}

	items, err := s.profiles.List()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"ok":     true,
		"active": s.profiles.ActiveName(),
		"items":  items,
	}, nil
}
