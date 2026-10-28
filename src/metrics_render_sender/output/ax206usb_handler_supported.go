//go:build linux || (windows && cgo)

package output

import (
	"context"
	"sync"
	"time"
)

const (
	defaultAX206ReconnectInterval = 3 * time.Second
	minAX206ReconnectInterval     = 100 * time.Millisecond
	maxAX206ReconnectInterval     = 60 * time.Second
)

func normalizeAX206ReconnectInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return defaultAX206ReconnectInterval
	}
	if interval < minAX206ReconnectInterval {
		return minAX206ReconnectInterval
	}
	if interval > maxAX206ReconnectInterval {
		return maxAX206ReconnectInterval
	}
	return interval
}

type AX206USBOutputHandler struct {
	ctx      context.Context
	cancel   context.CancelFunc
	deviceMu sync.RWMutex
	device   *AX206USB
	rgb565   *ImageRGB565

	stopOnce sync.Once
	stopCh   chan struct{}
	loopWg   sync.WaitGroup

	reconnectCh chan struct{}
	frameCh     chan *OutputFrame

	lastConnectErrMu sync.Mutex
	lastConnectErrAt time.Time

	lastTransferErrMu sync.Mutex
	lastTransferErrAt time.Time

	reconnectIntervalMu sync.RWMutex
	reconnectInterval   time.Duration
}

func NewAX206USBOutputHandler(cfg OutputConfig) (*AX206USBOutputHandler, error) {
	ctx, cancel := context.WithCancel(context.Background())
	handler := &AX206USBOutputHandler{
		ctx:               ctx,
		cancel:            cancel,
		stopCh:            make(chan struct{}),
		reconnectCh:       make(chan struct{}, 1),
		frameCh:           make(chan *OutputFrame, 1),
		reconnectInterval: normalizeAX206ReconnectInterval(time.Duration(normalizeAX206ReconnectMS(cfg.ReconnectMS)) * time.Millisecond),
	}
	handler.loopWg.Add(2)
	go handler.connectionLoop()
	go handler.outputLoop()
	return handler, nil
}

func (h *AX206USBOutputHandler) GetType() string {
	return TypeAX206USB
}

func (h *AX206USBOutputHandler) OutputFrame(frame *OutputFrame) error {
	if frame == nil {
		return nil
	}
	enqueueLatestAX206Frame(h.frameCh, frame)
	return nil
}

func (h *AX206USBOutputHandler) Close() error {
	h.stopOnce.Do(func() {
		close(h.stopCh)
		h.cancel()
		h.loopWg.Wait()
		h.detachDevice("Disconnected", nil)
	})
	return nil
}

func (h *AX206USBOutputHandler) UpdateConfig(cfg OutputConfig) {
	if h == nil {
		return
	}
	interval := normalizeAX206ReconnectInterval(time.Duration(normalizeAX206ReconnectMS(cfg.ReconnectMS)) * time.Millisecond)
	h.reconnectIntervalMu.Lock()
	h.reconnectInterval = interval
	h.reconnectIntervalMu.Unlock()
}

func (h *AX206USBOutputHandler) reconnectDelay() time.Duration {
	if h == nil {
		return defaultAX206ReconnectInterval
	}
	h.reconnectIntervalMu.RLock()
	defer h.reconnectIntervalMu.RUnlock()
	return normalizeAX206ReconnectInterval(h.reconnectInterval)
}

func (h *AX206USBOutputHandler) connectionLoop() {
	defer h.loopWg.Done()

	h.tryConnect()
	for {
		timer := time.NewTimer(h.reconnectDelay())
		select {
		case <-h.stopCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-h.reconnectCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			h.tryConnect()
		case <-timer.C:
			h.tryConnect()
		}
	}
}

func (h *AX206USBOutputHandler) outputLoop() {
	defer h.loopWg.Done()
	for {
		select {
		case <-h.stopCh:
			return
		case frame := <-h.frameCh:
			device := h.getDevice()
			if device == nil || frame == nil || frame.Image == nil {
				continue
			}
			startedAt := time.Now()
			h.rgb565 = frame.RGB565(h.rgb565)
			err := device.Blit(h.rgb565)
			recordAX206DeviceFrameRuntime(time.Since(startedAt), err)
			if err != nil {
				h.handleTransferFailure(device, err)
			}
		}
	}
}

func enqueueLatestAX206Frame(ch chan *OutputFrame, frame *OutputFrame) {
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

func (h *AX206USBOutputHandler) triggerReconnect() {
	select {
	case h.reconnectCh <- struct{}{}:
	default:
	}
}

func (h *AX206USBOutputHandler) getDevice() *AX206USB {
	h.deviceMu.RLock()
	defer h.deviceMu.RUnlock()
	return h.device
}

func (h *AX206USBOutputHandler) tryConnect() {
	if h.ctx.Err() != nil || h.getDevice() != nil {
		return
	}

	device, err := newAX206USBWithContext(h.ctx)
	if err != nil {
		h.logConnectFailure(err)
		return
	}

	if err := device.Brightness(7); err != nil {
		device.Close()
		h.logConnectFailure(err)
		return
	}

	h.deviceMu.Lock()
	if h.device != nil {
		h.deviceMu.Unlock()
		device.Close()
		return
	}
	h.device = device
	h.deviceMu.Unlock()
	logInfoModule("ax206usb", "Connected (%dx%d)", device.Width, device.Height)
}

func (h *AX206USBOutputHandler) logConnectFailure(err error) {
	if err == nil {
		return
	}
	h.lastConnectErrMu.Lock()
	defer h.lastConnectErrMu.Unlock()
	if time.Since(h.lastConnectErrAt) < 10*time.Second {
		return
	}
	h.lastConnectErrAt = time.Now()
	logWarnModule("ax206usb", "Connect failed, will retry: %v", err)
}

func (h *AX206USBOutputHandler) handleTransferFailure(failedDevice *AX206USB, err error) {
	h.lastTransferErrMu.Lock()
	shouldLog := time.Since(h.lastTransferErrAt) >= 3*time.Second
	if shouldLog {
		h.lastTransferErrAt = time.Now()
	}
	h.lastTransferErrMu.Unlock()
	if shouldLog {
		logWarnModule("ax206usb", "Transfer failed, reconnect scheduled: %v", err)
	}
	h.detachSpecificDevice(failedDevice, "Disconnected", err)
	h.triggerReconnect()
}

func (h *AX206USBOutputHandler) detachSpecificDevice(target *AX206USB, reason string, err error) {
	h.deviceMu.Lock()
	if h.device == nil {
		h.deviceMu.Unlock()
		return
	}
	if target != nil && h.device != target {
		h.deviceMu.Unlock()
		return
	}
	device := h.device
	h.device = nil
	h.deviceMu.Unlock()
	device.Close()
	if err != nil {
		logInfoModule("ax206usb", "%s: %v", reason, err)
		return
	}
	logInfoModule("ax206usb", "%s", reason)
}

func (h *AX206USBOutputHandler) detachDevice(reason string, err error) {
	h.detachSpecificDevice(nil, reason, err)
}
