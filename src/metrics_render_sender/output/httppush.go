package output

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type HTTPPushOutputHandler struct {
	cfg      OutputConfig
	typeName string
	client   *http.Client
	ctx      context.Context
	cancel   context.CancelFunc

	stopOnce sync.Once
	stopCh   chan struct{}
	loopWg   sync.WaitGroup
	frameCh  chan *OutputFrame

	lastErrorMu sync.Mutex
	lastErrorAt time.Time
}

func NewHTTPPushOutputHandler(cfg OutputConfig, typeName string) *HTTPPushOutputHandler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	ctx, cancel := context.WithCancel(context.Background())
	handler := &HTTPPushOutputHandler{
		ctx:      ctx,
		cancel:   cancel,
		cfg:      cfg,
		typeName: typeName,
		client: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.TimeoutMS) * time.Millisecond,
		},
		stopCh:  make(chan struct{}),
		frameCh: make(chan *OutputFrame, 1),
	}
	handler.loopWg.Add(1)
	go handler.loop()
	return handler
}

func (h *HTTPPushOutputHandler) GetType() string {
	return h.typeName
}

func (h *HTTPPushOutputHandler) OutputFrame(frame *OutputFrame) error {
	if frame == nil {
		return nil
	}
	enqueueLatestHTTPPushFrame(h.frameCh, frame)
	return nil
}

func (h *HTTPPushOutputHandler) Close() error {
	h.stopOnce.Do(func() {
		close(h.stopCh)
		h.cancel()
		h.loopWg.Wait()
		h.client.CloseIdleConnections()
	})
	return nil
}

func (h *HTTPPushOutputHandler) loop() {
	defer h.loopWg.Done()
	for {
		select {
		case <-h.stopCh:
			return
		case frame := <-h.frameCh:
			h.push(frame)
		}
	}
}

func (h *HTTPPushOutputHandler) push(frame *OutputFrame) {
	if frame == nil {
		return
	}
	body, contentType, encodeErr := h.encodeFrame(frame)
	if encodeErr != nil {
		h.logError("encode failed: %v", encodeErr)
		recordHTTPPushRuntime(h.typeName, 0, encodeErr)
		return
	}

	startedAt := time.Now()
	err := h.doRequest(body, contentType)
	recordHTTPPushRuntime(h.typeName, time.Since(startedAt), err)
	if err != nil {
		h.logError("push failed: %v", err)
	}
}

func (h *HTTPPushOutputHandler) doRequest(body []byte, contentType string) error {
	if strings.TrimSpace(h.cfg.URL) == "" {
		return fmt.Errorf("url is empty")
	}
	ctx, cancel := context.WithTimeout(h.ctx, h.client.Timeout)
	defer cancel()

	payload, requestContentType, err := h.buildRequestPayload(body, contentType)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, h.cfg.Method, h.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if requestContentType != "" {
		req.Header.Set("Content-Type", requestContentType)
	}
	h.applyAuth(req)
	for _, header := range h.cfg.Headers {
		req.Header.Set(header.Key, header.Value)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if !h.isSuccessStatus(resp.StatusCode) {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}
	return nil
}

func (h *HTTPPushOutputHandler) buildRequestPayload(body []byte, encodedContentType string) ([]byte, string, error) {
	switch h.cfg.BodyMode {
	case "multipart":
		return h.buildMultipartPayload(body, encodedContentType)
	default:
		return body, h.binaryContentType(encodedContentType), nil
	}
}

func (h *HTTPPushOutputHandler) buildMultipartPayload(body []byte, encodedContentType string) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)

	for _, field := range h.cfg.FormFields {
		if err := writer.WriteField(field.Key, field.Value); err != nil {
			return nil, "", err
		}
	}

	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipartValue(h.cfg.FileField), escapeMultipartValue(h.fileName())))
	partHeader.Set("Content-Type", h.filePartContentType(encodedContentType))
	partWriter, err := writer.CreatePart(partHeader)
	if err != nil {
		return nil, "", err
	}
	if _, err := partWriter.Write(body); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func (h *HTTPPushOutputHandler) binaryContentType(encodedContentType string) string {
	if strings.TrimSpace(h.cfg.ContentType) != "" {
		return h.cfg.ContentType
	}
	return encodedContentType
}

func (h *HTTPPushOutputHandler) filePartContentType(encodedContentType string) string {
	if strings.TrimSpace(h.cfg.ContentType) != "" {
		return h.cfg.ContentType
	}
	return encodedContentType
}

func (h *HTTPPushOutputHandler) fileName() string {
	if strings.TrimSpace(h.cfg.FileName) != "" {
		return filepath.Base(h.cfg.FileName)
	}
	switch h.cfg.Format {
	case "png":
		return "frame.png"
	default:
		return "frame.jpg"
	}
}

func (h *HTTPPushOutputHandler) applyAuth(req *http.Request) {
	switch h.cfg.AuthType {
	case "basic":
		req.SetBasicAuth(h.cfg.AuthUsername, h.cfg.AuthPassword)
	case "bearer":
		if strings.TrimSpace(h.cfg.AuthToken) != "" {
			req.Header.Set("Authorization", "Bearer "+h.cfg.AuthToken)
		}
	}
}

func (h *HTTPPushOutputHandler) isSuccessStatus(statusCode int) bool {
	if len(h.cfg.SuccessCodes) == 0 {
		return statusCode >= 200 && statusCode < 300
	}
	for _, code := range h.cfg.SuccessCodes {
		if code == statusCode {
			return true
		}
	}
	return false
}

func (h *HTTPPushOutputHandler) encodeFrame(frame *OutputFrame) ([]byte, string, error) {
	switch h.cfg.Format {
	case "png":
		data, err := frame.PNG()
		if err != nil {
			return nil, "", err
		}
		return data, "image/png", nil
	case "jpeg_baseline":
		data, err := frame.JPEGBaseline(h.cfg.Quality)
		if err != nil {
			return nil, "", err
		}
		return data, "image/jpeg", nil
	default:
		data, err := frame.JPEG(h.cfg.Quality)
		if err != nil {
			return nil, "", err
		}
		return data, "image/jpeg", nil
	}
}

func enqueueLatestHTTPPushFrame(ch chan *OutputFrame, frame *OutputFrame) {
	select {
	case ch <- frame:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- frame:
	default:
	}
}

func escapeMultipartValue(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"")
	return replacer.Replace(value)
}

func (h *HTTPPushOutputHandler) logError(format string, args ...interface{}) {
	h.lastErrorMu.Lock()
	defer h.lastErrorMu.Unlock()
	if time.Since(h.lastErrorAt) < 3*time.Second {
		return
	}
	h.lastErrorAt = time.Now()
	logWarnModule("httppush", format, args...)
}
