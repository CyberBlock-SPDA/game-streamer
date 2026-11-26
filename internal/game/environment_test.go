package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

func TestCompleteAccessUnits(t *testing.T) {
	units := [][]byte{{0, 0, 1, 0x67, 1, 0, 0, 1, 0x68, 2, 0, 0, 1, 0x65, 3}, {0, 0, 0, 1, 0x41, 4}}
	var b bytes.Buffer
	for _, u := range units {
		binary.Write(&b, binary.BigEndian, uint32(len(u)))
		b.Write(u)
	}
	for _, u := range units {
		v, e := NextUnit(&b)
		if e != nil || !bytes.Equal(v, u) {
			t.Fatal(v, e)
		}
	}
	for _, invalid := range [][]byte{{0, 0, 0, 5, 0, 0, 1}, {0, 0, 0, 4, 1, 2, 3, 4}, {255, 255, 255, 255}} {
		if _, e := NextUnit(bytes.NewReader(invalid)); e == nil {
			t.Fatal(invalid)
		}
	}
	if _, e := NextUnit(&b); !errors.Is(e, io.EOF) {
		t.Fatal(e)
	}
}

func TestCriticalChildExit(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	p := NewProcesses(cancel)
	if _, err := p.start("sh", []string{"-c", "exit 7"}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("child exit unnoticed")
	}
	stop, end := context.WithTimeout(t.Context(), time.Second)
	defer end()
	p.Stop(stop)
}
