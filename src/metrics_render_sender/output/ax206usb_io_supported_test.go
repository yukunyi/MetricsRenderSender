//go:build linux || (windows && cgo)

package output

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"
)

type blockingUSBEndpoint struct {
	started chan struct{}
}

func (e *blockingUSBEndpoint) WriteContext(ctx context.Context, data []byte) (int, error) {
	return len(data), nil
}

func (e *blockingUSBEndpoint) ReadContext(ctx context.Context, data []byte) (int, error) {
	close(e.started)
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestAX206CommandHasFiniteDeadline(t *testing.T) {
	device := &AX206USB{}
	started := time.Now()
	ctx, cancel := device.commandContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(started) || deadline.After(time.Now().Add(defaultAX206TransferTimeout)) {
		t.Fatalf("invalid command deadline: %v (present=%v)", deadline, ok)
	}
}

func TestAX206CommandHonorsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	endpoint := &blockingUSBEndpoint{started: make(chan struct{})}
	device := &AX206USB{transferCtx: ctx, outEndp: endpoint, inEndp: endpoint}
	if err := device.Brightness(7); err == nil {
		t.Fatal("stalled ACK succeeded")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("command did not wait for parent deadline: %v", ctx.Err())
	}
}

func TestAX206CloseCancelsActiveTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	endpoint := &blockingUSBEndpoint{started: make(chan struct{})}
	handler := &AX206USBOutputHandler{
		ctx: ctx, cancel: cancel,
		stopCh: make(chan struct{}), frameCh: make(chan *OutputFrame, 1),
		device: &AX206USB{transferCtx: ctx, outEndp: endpoint, inEndp: endpoint},
	}
	handler.loopWg.Add(1)
	go handler.outputLoop()
	defer handler.Close()
	_ = handler.OutputFrame(NewOutputFrame(image.NewRGBA(image.Rect(0, 0, 1, 1))))
	select {
	case <-endpoint.started:
	case <-time.After(time.Second):
		t.Fatal("USB ACK read did not start")
	}
	closed := make(chan struct{})
	go func() { _ = handler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel active USB transfer")
	}
}
