package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
)

var ErrEnded = errors.New("session ended")

type lifecycle struct {
	mu        sync.Mutex
	phase     string
	deadline  time.Time
	rtc, data bool
	c         config.Config
}

func (l *lifecycle) transition(phase string, timeout time.Duration) {
	l.phase = phase
	l.deadline = time.Time{}
	if timeout > 0 {
		l.deadline = time.Now().Add(timeout)
	}
	slog.Info("session state", "state", phase)
}

func (l *lifecycle) ready() { l.mu.Lock(); defer l.mu.Unlock(); l.transition("READY", l.c.Idle) }
func (l *lifecycle) claim() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.phase != "READY" {
		return false
	}
	l.transition("SIGNALING", l.c.Signal)
	return true
}

func (l *lifecycle) connecting() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.phase == "SIGNALING" {
		l.transition("CONNECTING", l.c.Connect)
	}
}

func (l *lifecycle) connected(isData bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if isData {
		l.data = true
	} else {
		l.rtc = true
	}
	if l.phase == "CONNECTING" && l.rtc && l.data {
		l.transition("PLAYING", 0)
	}
}

func (l *lifecycle) status() string { l.mu.Lock(); defer l.mu.Unlock(); return l.phase }
func (l *lifecycle) deadlineError(now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.deadline.IsZero() && !now.Before(l.deadline) {
		return fmt.Errorf("%w: %s timeout", ErrEnded, l.phase)
	}
	return nil
}

func (s *Server) Monitor(ctx context.Context) error {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case now := <-t.C:
			if err := s.life.deadlineError(now); err != nil {
				return err
			}
			lost, err := s.controls.Tick(now, s.life.c.Focus)
			if err != nil {
				return err
			}
			if lost {
				return fmt.Errorf("%w: focus timeout", ErrEnded)
			}
		}
	}
}
