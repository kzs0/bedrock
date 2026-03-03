package internal

import (
	"testing"
)

func TestGetBuffer(t *testing.T) {
	buf := GetBuffer()
	if buf == nil {
		t.Fatal("GetBuffer returned nil")
	}
	if buf.Len() != 0 {
		t.Error("GetBuffer should return a reset buffer")
	}

	// Write some data
	buf.WriteString("hello")
	if buf.Len() != 5 {
		t.Errorf("expected length 5, got %d", buf.Len())
	}

	PutBuffer(buf)
}

func TestPutBuffer_Large(t *testing.T) {
	buf := GetBuffer()
	// Write more than 64KB to exceed the pool limit
	data := make([]byte, 65*1024+1)
	buf.Write(data)

	// Should not panic; just doesn't pool it
	PutBuffer(buf)
}

func TestGetBuffer_Reuse(t *testing.T) {
	buf1 := GetBuffer()
	buf1.WriteString("test")
	PutBuffer(buf1)

	// Get another buffer - it should be reset
	buf2 := GetBuffer()
	if buf2.Len() != 0 {
		t.Error("reused buffer should be reset")
	}
	PutBuffer(buf2)
}

func TestGetSlice(t *testing.T) {
	s := GetSlice()
	if s == nil {
		t.Fatal("GetSlice returned nil")
	}
	if len(*s) != 0 {
		t.Error("GetSlice should return an empty slice")
	}

	// Append some data
	*s = append(*s, 1, 2, 3)
	if len(*s) != 3 {
		t.Errorf("expected length 3, got %d", len(*s))
	}

	PutSlice(s)
}

func TestPutSlice_Large(t *testing.T) {
	s := GetSlice()
	data := make([]byte, 65*1024+1)
	*s = append(*s, data...)

	// Should not panic; just doesn't pool it
	PutSlice(s)
}

func TestGetSlice_Reuse(t *testing.T) {
	s1 := GetSlice()
	*s1 = append(*s1, 1, 2, 3)
	PutSlice(s1)

	s2 := GetSlice()
	if len(*s2) != 0 {
		t.Error("reused slice should be empty")
	}
	PutSlice(s2)
}
