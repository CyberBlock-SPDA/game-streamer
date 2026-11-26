package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
	"github.com/CyberBlock-SPDA/game-streamer/internal/game"
	"github.com/CyberBlock-SPDA/game-streamer/internal/input"
	"github.com/CyberBlock-SPDA/game-streamer/internal/platform"
	"github.com/CyberBlock-SPDA/game-streamer/internal/stream"
)

func run(parent context.Context, c config.Config) (retErr error) {
	ctx, fail := context.WithCancelCause(parent)
	defer fail(nil)
	initCtx, endInit := context.WithTimeout(ctx, c.Init)
	defer endInit()
	sdk, err := platform.Open(c)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), c.Grace)
		defer cancel()
		if err := sdk.Shutdown(shutdown); err != nil {
			retErr = fmt.Errorf("Agones Shutdown: %w (session outcome: %v)", err, retErr)
		}
		slog.Info("session state", "state", "TERMINATED")
	}()
	if err = sdk.Configure(initCtx, &c); err != nil {
		return err
	}
	if err = os.Setenv("DISPLAY", c.Display); err != nil {
		return err
	}
	processes := game.NewProcesses(fail)
	var workers sync.WaitGroup
	var pipe *os.File
	defer func() {
		fail(retErr)
		grace, cancel := context.WithTimeout(context.Background(), c.Grace)
		defer cancel()
		processes.Stop(grace)
		if pipe != nil {
			_ = pipe.Close()
		}
		workers.Wait()
	}()
	if err = processes.StartGame(initCtx, c); err != nil {
		return err
	}
	device, err := input.Open(initCtx, c.Display)
	if err != nil {
		return err
	}
	defer device.Close()
	controls := input.New(device, c.Heartbeat)
	pipe, encoder, err := processes.Capture(c)
	if err != nil {
		return err
	}
	var lastIDR atomic.Int64
	keyframe := func() {
		now := time.Now().UnixNano()
		prev := lastIDR.Load()
		if now-prev >= int64(100*time.Millisecond) && lastIDR.CompareAndSwap(prev, now) {
			_ = encoder.Signal(syscall.SIGUSR1)
		}
	}
	server, err := stream.New(ctx, c, controls, sdk.Session, keyframe, fail)
	if err != nil {
		return err
	}
	defer func() { fail(retErr); server.Close() }()
	first := make(chan struct{})
	var latest atomic.Int64
	workers.Go(func() {
		for count := 0; ; count++ {
			data, err := game.NextUnit(pipe)
			if err != nil {
				fail(fmt.Errorf("encoder stream: %w", err))
				return
			}
			if err = server.Sample(data); err != nil {
				fail(err)
				return
			}
			latest.Store(time.Now().UnixNano())
			if count == 2 {
				close(first)
			}
		}
	})
	select {
	case <-initCtx.Done():
		return context.Cause(initCtx)
	case <-first:
	}
	tcp, err := net.Listen("tcp4", ":"+strconv.Itoa(c.SignalingPort))
	if err != nil {
		return err
	}
	defer tcp.Close()
	if c.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return err
		}
		tcp = tls.NewListener(tcp, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		s := server.Status()
		if ctx.Err() != nil || s == "INITIALIZING" || s == "SHUTTING_DOWN" {
			w.WriteHeader(503)
		}
		fmt.Fprintln(w, s)
	})
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	defer httpServer.Close()
	if err = initCtx.Err(); err != nil {
		return context.Cause(initCtx)
	}
	if err = sdk.Ready(initCtx); err != nil {
		return err
	}
	endInit()
	server.Ready()
	slog.Info("listeners ready", "signaling_tcp", c.SignalingPort, "webrtc_udp", c.WebRTCPort, "public_ip", c.PublicIP)
	workers.Go(func() {
		if err := httpServer.Serve(tcp); !errors.Is(err, http.ErrServerClosed) {
			fail(err)
		}
	})
	workers.Go(func() { t := time.NewTicker(c.Health); defer t.Stop(); fail(sdk.Health(ctx, t.C)) })
	workers.Go(func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if now.Sub(time.Unix(0, latest.Load())) > 4*time.Second {
					fail(errors.New("capture stalled"))
					return
				}
			}
		}
	})
	return server.Monitor(ctx)
}

func main() {
	level := new(slog.LevelVar)
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			slog.Error("invalid LOG_LEVEL")
			os.Exit(1)
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	c, err := config.Load()
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		slog.Info("session state", "state", "INITIALIZING")
		err = run(ctx, c)
	}
	if err != nil {
		if errors.Is(err, stream.ErrEnded) || errors.Is(err, context.Canceled) {
			slog.Info("session finished", "reason", err)
			return
		}
		slog.Error("agent failed", "error", err)
		os.Exit(1)
	}
}
