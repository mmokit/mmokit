package engine

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestTickProfileConcurrentRecordResetAndStats(t *testing.T) {
	tp := NewTickProfile([]string{"first", "second"})

	const iterations = 2000
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= iterations; i++ {
			d := time.Duration(i) * time.Nanosecond
			tp.Record([]time.Duration{d, 2 * d}, 3*d)
			if i%113 == 0 {
				tp.Reset()
			}
		}
	}()

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range iterations {
				stats := tp.Stats()
				if len(stats.SystemNames) != 2 {
					errs <- fmt.Errorf("system names length = %d, want 2", len(stats.SystemNames))
					return
				}
				if stats.SampleCount == 0 {
					continue
				}
				if len(stats.Systems) != 2 {
					errs <- fmt.Errorf("systems length = %d, want 2", len(stats.Systems))
					return
				}
				if stats.Systems[1].Latest != 2*stats.Systems[0].Latest ||
					stats.Total.Latest != 3*stats.Systems[0].Latest {
					errs <- fmt.Errorf("incoherent latest sample: %+v", stats)
					return
				}
				_ = tp.CachedStats()
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
