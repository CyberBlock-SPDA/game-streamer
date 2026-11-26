package input

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

type recorder struct {
	held  map[string]bool
	moves [][2]int
}

func (r *recorder) Set(k string, on bool) error {
	if on {
		r.held[k] = true
	} else {
		delete(r.held, k)
	}
	return nil
}

func (r *recorder) Move(x, y int) error { r.moves = append(r.moves, [2]int{x, y}); return nil }
func TestUnreliableInput(t *testing.T) {
	d := &recorder{held: map[string]bool{}}
	c := New(d, time.Second)
	now := time.Now()
	send := func(m string) {
		t.Helper()
		if err := c.Apply([]byte(m), now); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(keys ...string) {
		t.Helper()
		want := map[string]bool{}
		for _, k := range keys {
			want[k] = true
		}
		if !reflect.DeepEqual(d.held, want) {
			t.Fatalf("held %v, want %v", d.held, want)
		}
	}
	send(`{"type":"key","code":"KeyW","action":"down","seq":2}`)
	send(`{"type":"mouse_button","button":"left","action":"down","seq":0}`)
	expect("KeyW", "left")
	send(`{"type":"key","code":"KeyW","action":"up","seq":1}`)
	expect("KeyW", "left")
	send(`{"type":"input_state","focused":true,"pressedKeys":["Space"],"pressedButtons":[],"seq":3}`)
	expect("Space")
	send(`{"type":"key","code":"KeyW","action":"down","seq":2}`)
	expect("Space")
	send(`{"type":"mouse_move","dx":-2,"dy":3,"seq":4}`)
	if len(d.moves) != 1 || d.moves[0] != [2]int{-2, 3} {
		t.Fatal(d.moves)
	}
	send(`{"type":"focus","focused":false,"seq":5}`)
	expect()
	end, err := c.Tick(now.Add(20*time.Second), 30*time.Second)
	if end || err != nil {
		t.Fatal(end, err)
	}
	end, err = c.Tick(now.Add(31*time.Second), 30*time.Second)
	if !end || err != nil {
		t.Fatal(end, err)
	}
	now = now.Add(32 * time.Second)
	send(`{"type":"input_state","focused":true,"pressedKeys":["ArrowUp"],"pressedButtons":["right"],"seq":6}`)
	expect("ArrowUp", "right")
	end, err = c.Tick(now.Add(2*time.Second), 30*time.Second)
	if end || err != nil {
		t.Fatal(end, err)
	}
	expect()
	send(`{"type":"key","code":"KeyW","action":"down","seq":7}`)
	expect()
	send(`{"type":"input_state","focused":true,"pressedKeys":["KeyW"],"pressedButtons":[],"seq":8}`)
	expect("KeyW")
	send(`{"type":"input_reset","seq":9}`)
	expect()
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	send(`{"type":"key","code":"KeyW","action":"down","seq":10}`)
	expect()
}

func TestRejectMalformedInput(t *testing.T) {
	for _, raw := range []string{`{}`, `{"type":"key","seq":-1,"code":"Space","action":"up"}`, `{"type":"key","seq":1,"code":"Unknown","action":"down"}`, `{"type":"focus","focused":null,"seq":1}`, `{"type":"input_reset","seq":9007199254740992}`, `{"type":"input_reset","seq":0,"more":true}`, `{"type":"mouse_move","seq":2,"dx":1001,"dy":0}`, `{"type":"input_state","seq":2,"focused":true,"pressedKeys":["KeyA","KeyA"],"pressedButtons":[]}`} {
		if _, ok := parse([]byte(raw)); ok {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestMouseBurstIsBounded(t *testing.T) {
	d := &recorder{held: map[string]bool{}}
	c := New(d, time.Second)
	now := time.Now()
	for i := range 500 {
		message := fmt.Appendf(nil, `{"type":"mouse_move","seq":%d,"dx":1,"dy":0}`, i)
		if err := c.Apply(message, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.moves) != 4 {
		t.Fatalf("injected %d moves from a simultaneous burst", len(d.moves))
	}
}
