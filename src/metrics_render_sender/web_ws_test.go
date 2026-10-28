package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

func TestWebSocketStoreShutdownAndRestart(t *testing.T) {
	newStore := func() *ConfigStore {
		return &ConfigStore{wsClients: make(map[*webSocketClient]struct{})}
	}
	store := newStore()
	server := echo.New()
	server.GET("/ws", func(c echo.Context) error { return serveWebSocket(c, store) })
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	dial := func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	readRuntime := func(conn *websocket.Conn) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var message wsRuntimeMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read initial runtime: %v", err)
		}
		if message.Type != "runtime" {
			t.Fatalf("unexpected message type: %s", message.Type)
		}
	}
	assertClosed := func(conn *websocket.Conn) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
					t.Fatal("websocket remained open after shutdown")
				}
				return
			}
		}
	}

	conn := dial()
	readRuntime(conn)
	store.wsMu.RLock()
	var registered *webSocketClient
	for client := range store.wsClients {
		registered = client
	}
	store.wsMu.RUnlock()
	if registered == nil {
		t.Fatal("client not registered")
	}
	store.closeWebSocketClients()
	assertClosed(conn)
	select {
	case <-registered.done:
	default:
		t.Fatal("client goroutines did not finish")
	}
	store.wsMu.RLock()
	remaining := len(store.wsClients)
	store.wsMu.RUnlock()
	if remaining != 0 {
		t.Fatalf("shutdown retained %d clients", remaining)
	}
	store.closeWebSocketClients()
	assertClosed(dial())

	// A fresh server store must accept clients after the old store has stopped.
	store = newStore()
	restarted := dial()
	readRuntime(restarted)
	store.closeWebSocketClients()
	assertClosed(restarted)
}
