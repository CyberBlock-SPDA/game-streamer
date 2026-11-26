package input

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

var buttons = map[string]byte{"left": 1, "middle": 2, "right": 3}
var keysyms = func() map[string]xproto.Keysym {
	m := map[string]xproto.Keysym{
		"ArrowLeft": 0xff51, "ArrowUp": 0xff52, "ArrowRight": 0xff53, "ArrowDown": 0xff54,
		"Escape": 0xff1b, "Enter": 0xff0d, "Tab": 0xff09, "Backspace": 0xff08, "Space": 32,
		"ControlLeft": 0xffe3, "ControlRight": 0xffe4, "ShiftLeft": 0xffe1, "ShiftRight": 0xffe2,
		"AltLeft": 0xffe9, "AltRight": 0xffea, "Comma": 44, "Period": 46, "Minus": 45, "Equal": 61,
	}
	for i := 'A'; i <= 'Z'; i++ {
		m["Key"+string(i)] = xproto.Keysym(i + 32)
	}
	for i := '0'; i <= '9'; i++ {
		m["Digit"+string(i)] = xproto.Keysym(i)
	}
	for i := 1; i <= 12; i++ {
		m[fmt.Sprintf("F%d", i)] = xproto.Keysym(0xffbd + i)
	}
	return m
}()

type X11 struct {
	transport net.Conn
	conn      *xgb.Conn
	codes     map[string]byte
}

func Open(ctx context.Context, display string) (*X11, error) {
	path := "/tmp/.X11-unix/X" + strings.TrimPrefix(display, ":")
	transport, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = transport.SetDeadline(deadline)
	}
	conn, err := xgb.NewConnNet(transport)
	if err != nil {
		transport.Close()
		return nil, err
	}
	if err = xtest.Init(conn); err != nil {
		conn.Close()
		return nil, err
	}
	setup := xproto.Setup(conn)
	mapping, err := xproto.GetKeyboardMapping(conn, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode)+1).Reply()
	if err != nil {
		conn.Close()
		return nil, err
	}
	x := &X11{transport: transport, conn: conn, codes: map[string]byte{}}
	for k, sym := range keysyms {
		for i := 0; i < len(mapping.Keysyms); i += int(mapping.KeysymsPerKeycode) {
			if mapping.Keysyms[i] == sym {
				x.codes[k] = byte(setup.MinKeycode) + byte(i/int(mapping.KeysymsPerKeycode))
				break
			}
		}
	}
	_ = transport.SetDeadline(time.Time{})
	return x, nil
}

func (x *X11) Close() { x.transport.Close(); x.conn.Close() }

// A wedged display must not hold the session mutex or delay shutdown indefinitely.
func (x *X11) inject(typ, detail byte, dx, dy int16) error {
	if err := x.transport.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	defer x.transport.SetDeadline(time.Time{})
	return xtest.FakeInputChecked(x.conn, typ, detail, 0, 0, dx, dy, 0).Check()
}
func (x *X11) Set(k string, down bool) error {
	typ := byte(xproto.KeyRelease)
	detail := x.codes[k]
	if n := buttons[k]; n != 0 {
		typ = xproto.ButtonRelease
		detail = n
	}
	if detail == 0 {
		return fmt.Errorf("unmapped X11 control %q", k)
	}
	if down {
		typ--
	}
	return x.inject(typ, detail, 0, 0)
}

func (x *X11) Move(dx, dy int) error {
	return x.inject(xproto.MotionNotify, 1, int16(dx), int16(dy))
}
