module github.com/kzs0/bedrock/compat/otlp

go 1.24.0

require (
	github.com/google/pprof v0.0.0-20250208200701-d0013a598941
	github.com/kzs0/bedrock v0.0.0
	go.opentelemetry.io/proto/otlp v1.8.0
	go.opentelemetry.io/proto/otlp/collector/profiles/v1development v0.2.0
	go.opentelemetry.io/proto/otlp/profiles/v1development v0.2.0
	google.golang.org/protobuf v1.36.10
)

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.2 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
	golang.org/x/text v0.28.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250825161204-c5933d9347a5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250825161204-c5933d9347a5 // indirect
	google.golang.org/grpc v1.75.1 // indirect
)

replace github.com/kzs0/bedrock => ../..
