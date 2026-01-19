package bedrock

import (
	"io"
	"log/slog"
	"sync"

	"github.com/kzs0/bedrock/attr"
	bloglog "github.com/kzs0/bedrock/log"
	"github.com/kzs0/bedrock/metric"
	"github.com/kzs0/bedrock/trace"
)

var (
	noopInstance *Bedrock
	noopOnce     sync.Once
)

// noopBedrock returns a singleton no-op Bedrock instance that does nothing.
// This is used when no Bedrock instance is found in the context.
func noopBedrock() *Bedrock {
	noopOnce.Do(func() {
		handler := bloglog.NewHandler(&bloglog.HandlerOptions{
			Level:  slog.LevelInfo,
			Output: io.Discard,
			Format: "json",
		})

		noopInstance = &Bedrock{
			config: Config{
				Service: "noop",
			},
			logger:     slog.New(handler),
			logBridge:  bloglog.NewBridge(slog.New(handler)),
			tracer:     trace.NewTracer(trace.TracerConfig{ServiceName: "noop"}),
			metrics:    metric.NewRegistry(),
			staticAttr: attr.NewSet(),
			isNoop:     true,
		}
	})
	return noopInstance
}
