//go:build linux

package output

import (
	"context"
	"fmt"
	"image"
	"image/color"

	"github.com/google/gousb"
)

const (
	ax206vid = 0x1908
	ax206pid = 0x0102

	usbCmdSetProperty = 0x01
	usbCmdBlit        = 0x12

	ax206interface = 0x00
	ax206endpOut   = 0x01
	ax206endpIn    = 0x81

	usbMassStorageCSWSize   = 13
	usbMassStorageCSWPassed = 0x00
)

type ColorRGB565 struct {
	C uint16
}

func (c ColorRGB565) RGBA() (r, g, b, a uint32) {
	r = uint32((c.C >> 11 & 0x1f) << 3)
	r |= r << 8
	g = uint32((c.C >> 5 & 0x3f) << 2)
	g |= g << 8
	b = uint32((c.C & 0x1f) << 3)
	b |= b << 8
	a = 0xffff
	return
}

func rgb565Model(c color.Color) color.Color {
	if _, ok := c.(ColorRGB565); ok {
		return c
	}
	r, g, b, _ := c.RGBA()
	return ColorRGB565{
		uint16((r&0xF800)>>0) |
			uint16((g&0xFC00)>>5) |
			uint16((b&0xFC00)>>(5+6))}
}

var RGB565Model color.Model = color.ModelFunc(rgb565Model)

type ImageRGB565 struct {
	Pix    []uint8
	Stride int
	Rect   image.Rectangle
}

func (p ImageRGB565) ColorModel() color.Model { return RGB565Model }
func (p ImageRGB565) Bounds() image.Rectangle { return p.Rect }

func (p ImageRGB565) At(x, y int) color.Color {
	return p.RGB565At(x, y)
}

func (p ImageRGB565) RGB565At(x, y int) ColorRGB565 {
	if !(image.Point{x, y}.In(p.Rect)) {
		return ColorRGB565{}
	}
	i := p.PixOffset(x, y)
	return ColorRGB565{uint16(p.Pix[i+0])<<8 | uint16(p.Pix[i+1])}
}

func (p ImageRGB565) PixOffset(x, y int) int {
	return (y-p.Rect.Min.Y)*p.Stride + (x-p.Rect.Min.X)*2
}

func (p ImageRGB565) Set(x, y int, c color.Color) {
	if !(image.Point{x, y}.In(p.Rect)) {
		return
	}
	i := p.PixOffset(x, y)
	c1 := RGB565Model.Convert(c).(ColorRGB565)
	p.Pix[i+0] = uint8(c1.C >> 8)
	p.Pix[i+1] = uint8(c1.C)
}

func (p ImageRGB565) SetRGB565(x, y int, c ColorRGB565) {
	if !(image.Point{x, y}.In(p.Rect)) {
		return
	}
	i := p.PixOffset(x, y)
	p.Pix[i+0] = uint8(c.C >> 8)
	p.Pix[i+1] = uint8(c.C)
}

func (p *ImageRGB565) PixRect() []byte {
	r := p.Rect
	bufSize := r.Dx() * r.Dy() * 2
	data := make([]byte, bufSize)
	py := 0
	dxb := r.Dx() * 2
	for y := r.Min.Y; y < r.Max.Y; y++ {
		start := p.PixOffset(r.Min.X, y)
		copy(data[py:], p.Pix[start:start+dxb])
		py += dxb
	}
	return data
}

func (p *ImageRGB565) Bytes() []byte {
	if p == nil {
		return nil
	}
	r := p.Rect
	size := r.Dx() * r.Dy() * 2
	if r.Min.X == 0 && r.Min.Y == 0 && p.Stride == r.Dx()*2 && len(p.Pix) >= size {
		return p.Pix[:size]
	}
	return p.PixRect()
}

type AX206USB struct {
	transferCtx context.Context
	Width       int
	Height      int
	Debug       bool

	ctx       *gousb.Context
	device    *gousb.Device
	config    *gousb.Config
	intf      *gousb.Interface
	outEndp   usbBulkWriter
	inEndp    usbBulkReader
	hasCtx    bool
	hasDevice bool
	hasConfig bool
	hasIntf   bool
}

func NewAX206USB() (*AX206USB, error) {
	return newAX206USBWithContext(context.Background())
}

func newAX206USBWithContext(ctxParent context.Context) (*AX206USB, error) {
	ax206 := &AX206USB{transferCtx: ctxParent}

	ctx := gousb.NewContext()
	if ctx == nil {
		return nil, fmt.Errorf("failed to create USB context")
	}
	ax206.ctx = ctx
	ax206.hasCtx = true

	device, err := ctx.OpenDeviceWithVIDPID(ax206vid, ax206pid)
	if err != nil {
		ax206.Close()
		return nil, fmt.Errorf("failed to open device: %v", err)
	}
	if device == nil {
		ax206.Close()
		return nil, fmt.Errorf("device is nil")
	}
	ax206.device = device
	ax206.hasDevice = true

	if ax206.Debug {
		logDebug("Device opened: %s", device)
	}

	config, err := device.Config(1)
	if err != nil {
		ax206.Close()
		return nil, fmt.Errorf("failed to get config: %v", err)
	}
	if config == nil {
		ax206.Close()
		return nil, fmt.Errorf("config is nil")
	}
	ax206.config = config
	ax206.hasConfig = true

	intf, err := config.Interface(ax206interface, 0)
	if err != nil {
		ax206.Close()
		return nil, fmt.Errorf("failed to get interface: %v", err)
	}
	if intf == nil {
		ax206.Close()
		return nil, fmt.Errorf("interface is nil")
	}
	ax206.intf = intf
	ax206.hasIntf = true

	outEndp, err := intf.OutEndpoint(ax206endpOut)
	if err != nil {
		ax206.Close()
		return nil, fmt.Errorf("failed to get out endpoint: %v", err)
	}
	ax206.outEndp = outEndp

	inEndp, err := intf.InEndpoint(ax206endpIn)
	if err != nil {
		ax206.Close()
		return nil, fmt.Errorf("failed to get in endpoint: %v", err)
	}
	ax206.inEndp = inEndp

	// Get actual device dimensions
	width, height, err := ax206.GetDimensions()
	if err != nil {
		// Fall back to default dimensions if query fails
		ax206.Width = 480
		ax206.Height = 320
		if ax206.Debug {
			logWarn("Failed to get device dimensions, using defaults: %v", err)
		}
	} else {
		ax206.Width = width
		ax206.Height = height
		if ax206.Debug {
			logDebug("Device dimensions: %dx%d", width, height)
		}
	}

	return ax206, nil
}

func (ax206 *AX206USB) GetDimensions() (width, height int, err error) {
	cmd := []byte{
		0xcd, 0, 0, 0,
		0, 2, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
	}
	data, err := ax206.scsiRead(cmd, 5)
	if err != nil {
		return 0, 0, err
	}
	if len(data) < 4 {
		return 0, 0, fmt.Errorf("insufficient data received")
	}
	width = int(data[0]) | int(data[1])<<8
	height = int(data[2]) | int(data[3])<<8
	return width, height, nil
}

func (ax206 *AX206USB) Brightness(lvl int) error {
	if lvl < 0 {
		lvl = 0
	}
	if lvl > 7 {
		lvl = 7
	}

	cmd := []byte{
		0xcd, 0, 0, 0,
		0, 6, usbCmdSetProperty,
		1, 0, // PROPERTY_BRIGHTNESS
		byte(lvl), byte(lvl >> 8),
		0, 0, 0, 0, 0,
	}

	return ax206.scsiWrite(cmd, nil)
}

func (ax206 *AX206USB) Blit(img *ImageRGB565) error {
	if img == nil {
		return fmt.Errorf("image is nil")
	}

	r := img.Rect
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return fmt.Errorf("image bounds are empty")
	}
	cmd := []byte{
		0xcd, 0, 0, 0,
		0, 6, usbCmdBlit,
		byte(r.Min.X), byte(r.Min.X >> 8),
		byte(r.Min.Y), byte(r.Min.Y >> 8),
		byte(r.Max.X - 1), byte((r.Max.X - 1) >> 8),
		byte(r.Max.Y - 1), byte((r.Max.Y - 1) >> 8),
		0,
	}
	return ax206.scsiWrite(cmd, img.Bytes())
}

func (ax206 *AX206USB) Close() {
	if ax206.hasIntf {
		ax206.intf.Close()
		ax206.hasIntf = false
	}
	if ax206.hasConfig {
		ax206.config.Close()
		ax206.hasConfig = false
	}
	if ax206.hasDevice {
		ax206.device.Close()
		ax206.hasDevice = false
	}
	if ax206.hasCtx {
		ax206.ctx.Close()
		ax206.hasCtx = false
	}
}

func (ax206 *AX206USB) scsiCmdPrepare(cmd []byte, blockLen int, out bool) []byte {
	var bmCBWFlags byte
	if out {
		bmCBWFlags = 0x00
	} else {
		bmCBWFlags = 0x80
	}
	buf := []byte{
		0x55, 0x53, 0x42, 0x43, // dCBWSignature
		0xde, 0xad, 0xbe, 0xef, // dCBWTag
		byte(blockLen), byte(blockLen >> 8), byte(blockLen >> 16), byte(blockLen >> 24), // dCBWLength (4 byte)
		bmCBWFlags,     // bmCBWFlags: 0x80: data in (dev to host), 0x00: Data out
		0x00,           // bCBWLUN
		byte(len(cmd)), // bCBWCBLength

		// SCSI cmd: (15)
		0xcd, 0x00, 0x00, 0x00,
		0x00, 0x06, 0x11, 0xf8,
		0x70, 0x00, 0x40, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}

	copy(buf[15:], cmd)

	if ax206.Debug {
		logDebug("SCSI cmd: %v", cmd)
		logDebug("SCSI command: %v", buf)
	}
	return buf
}

func (ax206 *AX206USB) scsiGetAck(ctx context.Context) error {
	buf := make([]byte, usbMassStorageCSWSize)
	// Get ACK
	if ax206.Debug {
		logDebug("[ACK] Read ACK from device")
	}
	n, err := readBulkFull(ctx, ax206.inEndp, buf)
	if err != nil {
		return fmt.Errorf("ACK read failed: %v", err)
	}
	if ax206.Debug {
		logDebug("[ACK] data %v", buf[:n])
	}

	if n != usbMassStorageCSWSize || string(buf[:4]) != "USBS" {
		return fmt.Errorf("got invalid reply")
	}
	if buf[12] != usbMassStorageCSWPassed {
		return fmt.Errorf("device reported CSW status %#02x", buf[12])
	}
	return nil
}

func (ax206 *AX206USB) scsiWrite(cmd []byte, data []byte) error {
	ctx, cancel := ax206.commandContext()
	defer cancel()
	// Write command to device
	if ax206.Debug {
		logDebug("[WRITE] Write command to device")
	}
	if err := writeBulkAll(ctx, ax206.outEndp, ax206.scsiCmdPrepare(cmd, len(data), true)); err != nil {
		return fmt.Errorf("command write failed: %v", err)
	}

	// Write data to device
	if data != nil {
		if ax206.Debug {
			logDebug("[WRITE] Write data to device")
		}
		if err := writeBulkAll(ctx, ax206.outEndp, data); err != nil {
			return fmt.Errorf("data write failed: %v", err)
		}
	}

	return ax206.scsiGetAck(ctx)
}

func (ax206 *AX206USB) scsiRead(cmd []byte, blockLen int) ([]byte, error) {
	ctx, cancel := ax206.commandContext()
	defer cancel()
	// Write command to device
	if ax206.Debug {
		logDebug("[READ] Write command to device")
	}
	if err := writeBulkAll(ctx, ax206.outEndp, ax206.scsiCmdPrepare(cmd, blockLen, false)); err != nil {
		return nil, fmt.Errorf("command write failed: %v", err)
	}

	if ax206.Debug {
		logDebug("[read] Read data from device")
	}
	// Read data from device
	data := make([]byte, blockLen)
	n, err := readBulkFull(ctx, ax206.inEndp, data)
	if err != nil {
		return nil, fmt.Errorf("data read failed: %v", err)
	}
	if ax206.Debug {
		logDebug("[read] data %v", data[:n])
	}

	err = ax206.scsiGetAck(ctx)
	if err != nil {
		return data[:n], err
	}

	return data[:n], nil
}
