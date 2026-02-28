package h2c

import (
	"testing"
)

func BenchmarkEncodeRequestHeaders(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = EncodeRequestHeaders("localhost:4317", "/opentelemetry.proto.collector.trace.v1.TraceService/Export", false)
	}
}

func BenchmarkEncodeRequestHeaders_TLS(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = EncodeRequestHeaders("collector.example.com:443", "/opentelemetry.proto.collector.trace.v1.TraceService/Export", true)
	}
}

// typicalTrailers is a minimal gRPC trailers block that the server might send back.
// "grpc-status: 0" encoded without Huffman, literal without indexing, indexed name (not in static table → new name).
var typicalTrailers = func() []byte {
	// Literal header field without indexing, new name: "grpc-status" = "0"
	return appendLiteralNewName(nil, "grpc-status", "0")
}()

func BenchmarkDecodeHeaders(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = DecodeHeaders(typicalTrailers)
	}
}

func BenchmarkDecodeHpackInt(b *testing.B) {
	data := []byte{0x8f} // indexed representation, value 15
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = decodeHpackInt(data, 7)
	}
}
