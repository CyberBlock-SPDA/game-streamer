package input

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/wire"
	"golang.org/x/time/rate"
)

type Device interface {
	Set(control string, down bool) error
	Move(dx, dy int) error
}

type Controller struct {
	mu                       sync.Mutex
	device                   Device
	mouseRate                *rate.Limiter
	down                     map[string]bool
	last                     map[string]int64
	floor, focusSeq, moveSeq int64
	focused                  bool
	lostAt, snapshot         time.Time
	heartbeat                time.Duration
	closed, expired          bool
}

func New(d Device, heartbeat time.Duration) *Controller {
	return &Controller{device: d, mouseRate: rate.NewLimiter(120, 4), down: map[string]bool{}, last: map[string]int64{}, floor: -1, focusSeq: -1, moveSeq: -1, focused: true, heartbeat: heartbeat}
}

type message struct {
	Type, Action, Code, Button  string
	Seq                         int64
	DX, DY                      int
	Focused                     bool
	PressedKeys, PressedButtons []string
}

var fields = map[string][]string{"key": {"code", "action"}, "mouse_button": {"button", "action"}, "mouse_move": {"dx", "dy"}, "focus": {"focused"}, "input_reset": {}, "input_state": {"pressedKeys", "pressedButtons", "focused"}}

func parse(raw []byte) (m message, ok bool) {
	if len(raw) > 8192 {
		return
	}
	f, exists := fields[wire.Type(raw)]
	if !exists {
		return
	}
	names := append([]string{"type", "seq"}, f...)
	if _, err := wire.Object(raw, names...); err != nil {
		return
	}
	if json.Unmarshal(raw, &m) != nil || m.Seq < 0 || m.Seq > 9007199254740991 {
		return
	}
	switch m.Type {
	case "key":
		ok = keysyms[m.Code] != 0 && (m.Action == "up" || m.Action == "down")
	case "mouse_button":
		ok = buttons[m.Button] != 0 && (m.Action == "up" || m.Action == "down")
	case "mouse_move":
		ok = m.DX >= -1000 && m.DX <= 1000 && m.DY >= -1000 && m.DY <= 1000
	case "input_state":
		if len(m.PressedKeys) > 64 || len(m.PressedButtons) > 3 {
			return
		}
		seen := map[string]bool{}
		for _, k := range m.PressedKeys {
			if keysyms[k] == 0 || seen[k] {
				return
			}
			seen[k] = true
		}
		for _, b := range m.PressedButtons {
			if buttons[b] == 0 || seen[b] {
				return
			}
			seen[b] = true
		}
		ok = true
	default:
		ok = true
	}
	return
}

func (c *Controller) set(k string, down bool) error {
	if c.down[k] == down {
		return nil
	}
	if down && buttons[k] == 0 && len(c.down) >= 64 {
		heldKeys := len(c.down)
		for button := range buttons {
			if c.down[button] {
				heldKeys--
			}
		}
		if heldKeys >= 64 {
			return nil
		}
	}
	if err := c.device.Set(k, down); err != nil {
		return err
	}
	if down {
		c.down[k] = true
	} else {
		delete(c.down, k)
	}
	return nil
}

func (c *Controller) release() error {
	var err error
	for k := range c.down {
		err = errors.Join(err, c.set(k, false))
	}
	return err
}

func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return c.release()
}

func (c *Controller) focus(value bool, seq int64, now time.Time) error {
	if seq <= c.focusSeq {
		return nil
	}
	c.focusSeq = seq
	if c.focused == value {
		return nil
	}
	c.focused = value
	if !value {
		c.lostAt = now
		return c.release()
	}
	c.lostAt = time.Time{}
	return nil
}

func (c *Controller) Apply(raw []byte, now time.Time) error {
	m, valid := parse(raw)
	if !valid {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || m.Seq <= c.floor {
		return nil
	}
	if c.snapshot.IsZero() {
		c.snapshot = now
	}
	switch m.Type {
	case "focus":
		return c.focus(m.Focused, m.Seq, now)
	case "input_state", "input_reset":
		desired := map[string]bool{}
		if m.Type == "input_state" {
			c.snapshot = now
			c.expired = false
			if err := c.focus(m.Focused, m.Seq, now); err != nil {
				return err
			}
			if c.focused && m.Focused && m.Seq >= c.focusSeq {
				for _, k := range m.PressedKeys {
					desired[k] = true
				}
				for _, b := range m.PressedButtons {
					desired[b] = true
				}
			}
		}
		c.floor = m.Seq
		for k := range c.down {
			if !desired[k] && c.last[k] <= m.Seq {
				if err := c.set(k, false); err != nil {
					return err
				}
			}
		}
		for k := range desired {
			if c.last[k] <= m.Seq {
				if err := c.set(k, true); err != nil {
					return err
				}
			}
		}
	case "mouse_move":
		if c.focused && !c.expired && m.Seq >= c.focusSeq && m.Seq > c.moveSeq {
			c.moveSeq = m.Seq
			if !c.mouseRate.AllowN(now, 1) {
				return nil
			}
			return c.device.Move(m.DX, m.DY)
		}
	default:
		k := m.Code
		if m.Type == "mouse_button" {
			k = m.Button
		}
		if seq, seen := c.last[k]; seen && m.Seq <= seq {
			return nil
		}
		if !c.focused || c.expired || m.Seq < c.focusSeq {
			return nil
		}
		c.last[k] = m.Seq
		return c.set(k, m.Action == "down")
	}
	return nil
}

// Tick releases stale input independently of session health; only prolonged blur ends play.
func (c *Controller) Tick(now time.Time, grace time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.snapshot.IsZero() && now.Sub(c.snapshot) >= c.heartbeat {
		c.expired = true
		if err := c.release(); err != nil {
			return false, err
		}
	}
	return !c.lostAt.IsZero() && now.Sub(c.lostAt) >= grace, nil
}
