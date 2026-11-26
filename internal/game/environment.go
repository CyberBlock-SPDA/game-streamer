package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
)

func wait(ctx context.Context, probe func() bool) error {
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if probe() {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-timer.C:
		}
	}
}

func (p *Processes) StartGame(ctx context.Context, c config.Config) error {
	info, err := os.Stat(c.IWAD)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("expected regular IWAD")
	}
	size := fmt.Sprintf("%dx%d", c.Width, c.Height)
	_, err = p.start("Xvfb", []string{c.Display, "-screen", "0", fmt.Sprintf("%sx%d", size, c.Depth), "-ac", "-nolisten", "tcp", "-noreset"}, nil, os.Stderr)
	if err != nil {
		return err
	}
	err = wait(ctx, func() bool {
		out, e := exec.CommandContext(ctx, "xdpyinfo", "-display", c.Display).Output()
		return e == nil && bytes.Contains(out, []byte(size+" pixels"))
	})
	if err != nil {
		return fmt.Errorf("X11 display: %w", err)
	}
	game, err := p.start(c.Game, []string{"-iwad", c.IWAD, "-window", "-width", strconv.Itoa(c.Width), "-height", strconv.Itoa(c.Height), "-nosound", "-nogui", "-warp", "1", "-skill", "2"}, nil, os.Stderr)
	if err != nil {
		return err
	}
	return wait(ctx, func() bool {
		out, e := exec.CommandContext(ctx, "xdotool", "search", "--onlyvisible", "--pid", strconv.Itoa(game.Pid)).Output()
		ids := strings.Fields(string(out))
		if e != nil || len(ids) == 0 {
			return false
		}
		geometry, err := exec.CommandContext(ctx, "xdotool", "getwindowgeometry", "--shell", ids[0]).Output()
		if err != nil {
			return false
		}
		dims := map[string]int{}
		for _, line := range strings.Fields(string(geometry)) {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				dims[key], _ = strconv.Atoi(value)
			}
		}
		if dims["WIDTH"] <= 0 || dims["HEIGHT"] <= 0 {
			return false
		}
		x := strconv.Itoa(max(0, (c.Width-dims["WIDTH"])/2))
		y := strconv.Itoa(max(0, (c.Height-dims["HEIGHT"])/2))
		return exec.CommandContext(ctx, "xdotool", "windowmove", ids[0], x, y, "windowfocus", ids[0]).Run() == nil
	})
}

// NextUnit decodes the length-delimited Annex B packets from libavcodec.
func NextUnit(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	if n < 4 || n > 4<<20 {
		return nil, fmt.Errorf("invalid access unit size %d", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte{0, 0, 1}) && !bytes.HasPrefix(data, []byte{0, 0, 0, 1}) {
		return nil, fmt.Errorf("expected Annex B start code")
	}
	return data, nil
}

func (p *Processes) Capture(c config.Config) (*os.File, *os.Process, error) {
	raw, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer raw.Close()
	defer writer.Close()
	stream, out, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer out.Close()
	fps := strconv.Itoa(c.FPS)
	encoder, err := p.start("game-encoder", []string{strconv.Itoa(c.Width), strconv.Itoa(c.Height), fps, c.Bitrate}, raw, out)
	if err != nil {
		stream.Close()
		return nil, nil, err
	}
	_, err = p.start("ffmpeg", []string{"-nostdin", "-hide_banner", "-loglevel", "warning", "-f", "x11grab", "-draw_mouse", "0", "-framerate", fps, "-video_size", fmt.Sprintf("%dx%d", c.Width, c.Height), "-i", c.Display + ".0", "-an", "-vf", "format=yuv420p", "-threads", "1", "-f", "rawvideo", "pipe:1"}, nil, writer)
	if err != nil {
		stream.Close()
		return nil, nil, err
	}
	return stream, encoder, nil
}
