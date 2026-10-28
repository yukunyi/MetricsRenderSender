//go:build linux || (windows && cgo)

package output

import (
	"context"
	"fmt"
	"time"
)

// Match the default network output timeout, while bounding the entire command.
const defaultAX206TransferTimeout = 5 * time.Second

type usbBulkWriter interface {
	WriteContext(context.Context, []byte) (int, error)
}

type usbBulkReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

func (ax206 *AX206USB) commandContext() (context.Context, context.CancelFunc) {
	parent := ax206.transferCtx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, defaultAX206TransferTimeout)
}

func writeBulkAll(ctx context.Context, endp usbBulkWriter, data []byte) error {
	for len(data) > 0 {
		n, err := endp.WriteContext(ctx, data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return fmt.Errorf("short write: wrote 0 of %d bytes", len(data))
		}
		data = data[n:]
	}
	return nil
}

func readBulkFull(ctx context.Context, endp usbBulkReader, data []byte) (int, error) {
	total := 0
	for total < len(data) {
		n, err := endp.ReadContext(ctx, data[total:])
		if err != nil {
			return total + n, err
		}
		if n <= 0 {
			return total, fmt.Errorf("short read: read %d of %d bytes", total, len(data))
		}
		total += n
	}
	return total, nil
}
