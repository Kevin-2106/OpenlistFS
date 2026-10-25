//go:build windows

package singleinstance

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestConcurrentAcquireAndActivationDuringStartup(t *testing.T) {
	id := fmt.Sprintf("OpenListFS-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	const count = 12
	var wg sync.WaitGroup
	primaries := make(chan *Instance, count)
	errors := make(chan error, count)
	for range count {
		wg.Go(func() {
			i, primary, err := Acquire(id)
			if err != nil {
				errors <- err
				return
			}
			if primary {
				primaries <- i
			}
		})
	}
	wg.Wait()
	close(primaries)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if len(primaries) != 1 {
		t.Fatalf("got %d primary instances", len(primaries))
	}
	i := <-primaries
	activated := make(chan struct{}, 1)
	i.Start(context.Background(), func() { activated <- struct{}{} })
	select {
	case <-activated:
	case <-time.After(3 * time.Second):
		t.Fatal("startup activation was lost")
	}
	i.Close()
	next, primary, err := Acquire(id)
	if err != nil || !primary {
		t.Fatalf("lock not released: primary=%v err=%v", primary, err)
	}
	next.Close()
}
