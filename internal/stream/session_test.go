package stream

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
)

func TestSessionDeadlines(t *testing.T) {
	l := lifecycle{c: config.Config{Idle: 3 * time.Second, Signal: time.Second, Connect: 2 * time.Second}}
	l.ready()
	if l.deadlineError(time.Now().Add(4*time.Second)) == nil {
		t.Fatal("idle deadline")
	}
	if !l.claim() || l.claim() {
		t.Fatal("admission")
	}
	if l.deadlineError(time.Now().Add(1500*time.Millisecond)) == nil {
		t.Fatal("signaling deadline")
	}
	l.connecting()
	l.connected(true)
	if l.status() != "CONNECTING" || l.deadlineError(time.Now().Add(3*time.Second)) == nil {
		t.Fatal("missing connection deadline")
	}
	l.connected(false)
	if l.status() != "PLAYING" || l.deadlineError(time.Now().Add(time.Hour)) != nil {
		t.Fatal("playing deadline")
	}
}

func TestOnlyOneConcurrentClaim(t *testing.T) {
	l := lifecycle{c: config.Config{Idle: time.Minute, Signal: time.Second}}
	l.ready()
	var claims atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wg.Go(func() {
			<-start
			if l.claim() {
				claims.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	if n := claims.Load(); n != 1 {
		t.Fatalf("admitted %d players", n)
	}
}
