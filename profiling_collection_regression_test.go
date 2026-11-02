package bedrock

import (
	"bytes"
	"context"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/kzs0/bedrock/attr"
)

func TestCPUCollectionDoesNotStopAnotherProfiler(t *testing.T) {
	var owned bytes.Buffer
	if err := pprof.StartCPUProfile(&owned); err != nil {
		t.Fatal(err)
	}
	defer pprof.StopCPUProfile()
	err := collectAndPushProfile(context.Background(), "svc", attr.EmptySet, "http://unused.invalid", "", time.Millisecond, ProfileTypeCPU)
	if err == nil {
		t.Fatal("concurrent CPU collection unexpectedly succeeded")
	}
	var challenger bytes.Buffer
	if err := pprof.StartCPUProfile(&challenger); err == nil {
		pprof.StopCPUProfile()
		t.Fatal("cloud collector stopped the existing profiler")
	}
}
func TestCloudRejectsInvalidCPUCollectionWindows(t *testing.T) {
	for _, duration := range []time.Duration{0, -time.Second} {
		t.Run(duration.String(), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid CPU collection duration accepted")
				}
			}()
			cfg := applyInitOptions([]InitOption{WithCloud("key", CloudProfileCPUSampleDuration(duration))})
			if cfg.cloudCfg == nil {
				t.Fatal("cloud configuration missing")
			}
		})
	}
}
