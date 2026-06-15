package degradation

import "io"

// bytesReadCloser 将 []byte 包装为 io.ReadCloser
type bytesReadCloser struct {
	*io.PipeReader
}

// NewBytesReadCloser 创建 byte 的 ReadCloser
func NewBytesReadCloser(data []byte) io.ReadCloser {
	r, w := io.Pipe()
	go func() {
		w.Write(data)
		w.Close()
	}()
	return &bytesReadCloser{r}
}
