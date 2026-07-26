package metrics

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEWMAConcurrentUpdateAndValue(t *testing.T) {
	e := NewEWMA(0.1)

	const iterations = 10000
	start := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := range iterations {
			e.Update(float64(i))
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range iterations {
				_ = e.Value()
			}
		}()
	}

	close(start)
	wg.Wait()
}

func TestCellMetricsConcurrentRecordSnapshotAndRename(t *testing.T) {
	nm := NewCellMetrics("cell_0_0", 20, nil, nil)

	const iterations = 5000
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 4)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 1; i <= iterations; i++ {
			nm.RecordTick(time.Duration(i)*time.Microsecond, i, i/2, i/3, i/4)
			if i%97 == 0 {
				nm.SetCellID(fmt.Sprintf("cell_%d_0", i))
			}
		}
	}()

	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range iterations {
				snap := nm.Snapshot()
				if snap.CellID == "" {
					errs <- fmt.Errorf("snapshot observed an empty cell ID")
					return
				}
				_ = nm.CellID()
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
