package output

import (
	"bufio"
	"image"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPPushCloseCancelsActiveRequest(t *testing.T) {
	handler := NewHTTPPushOutputHandler(OutputConfig{URL: "http://localhost/upload", Method: "POST", TimeoutMS: 600000}, TypeHTTPPush)
	started := make(chan struct{})
	handler.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	defer handler.Close()
	_ = handler.OutputFrame(NewOutputFrame(image.NewRGBA(image.Rect(0, 0, 1, 1))))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	closed := make(chan struct{})
	go func() { _ = handler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active HTTP request")
	}
}

func TestTCPPushCloseCancelsActiveRequest(t *testing.T) {
	handler := NewTCPPushOutputHandler(OutputConfig{URL: "tcp://localhost:9100", UploadToken: "token", TimeoutMS: 600000}, TypeTCPPush)
	client, server := net.Pipe()
	defer server.Close()
	defer handler.Close()
	handler.connMu.Lock()
	handler.conn = client
	handler.reader = bufio.NewReader(client)
	handler.lastUsed = time.Now()
	handler.connMu.Unlock()
	started := make(chan struct{})
	go func() {
		_, _ = readTCPPushTestRequest(bufio.NewReader(server))
		close(started)
	}()
	_ = handler.OutputFrame(NewOutputFrame(image.NewRGBA(image.Rect(0, 0, 1, 1))))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	closed := make(chan struct{})
	go func() { _ = handler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt active TCP request")
	}
	if _, err := handler.PingStatus(); err == nil {
		t.Fatal("closed TCP handler accepted a new request")
	}
}
