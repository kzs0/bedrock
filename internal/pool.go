package internal

import (
	"bytes"
	"sync"
)

// BufferPool provides a pool of reusable byte buffers.
var BufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// GetBuffer retrieves a buffer from the pool.
func GetBuffer() *bytes.Buffer {
	buf := BufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// PutBuffer returns a buffer to the pool.
func PutBuffer(buf *bytes.Buffer) {
	if buf.Cap() > 64*1024 {
		// Don't pool very large buffers
		return
	}
	BufferPool.Put(buf)
}

// SlicePool provides a pool of reusable byte slices.
var SlicePool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1024)
		return &b
	},
}

// GetSlice retrieves a byte slice from the pool.
func GetSlice() *[]byte {
	s := SlicePool.Get().(*[]byte)
	*s = (*s)[:0]
	return s
}

// PutSlice returns a byte slice to the pool.
func PutSlice(s *[]byte) {
	if cap(*s) > 64*1024 {
		return
	}
	SlicePool.Put(s)
}
