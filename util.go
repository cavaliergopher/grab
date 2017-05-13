package grab

import (
	"context"
	"io"
)

// isCanceled returns a non-nil error if the given context has been canceled.
func isCanceled(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// copyBuffer behaves similarly to io.CopyBuffer except that is checks for
// cancelation of the given context.Context.
func copyBuffer(ctx context.Context, dst io.Writer, src io.Reader, buf []byte) (written int64, err error) {
	if buf == nil {
		buf = make([]byte, 32*1024)
	}
	for {
		if err = isCanceled(ctx); err != nil {
			return
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			if err = isCanceled(ctx); err != nil {
				return
			}
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}
