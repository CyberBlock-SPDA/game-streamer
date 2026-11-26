package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Display, Game, IWAD, Bitrate, PublicIP, Origin, SessionID, SDKAddress, TLSCert, TLSKey string
	Width, Height, Depth, FPS, SignalingPort, WebRTCPort                                   int
	Init, Idle, Signal, Connect, Heartbeat, Focus, Grace, Health                           time.Duration
	Standalone                                                                             bool
	PublicKey                                                                              ed25519.PublicKey
}

func env(k, v string) string {
	if s, ok := os.LookupEnv(k); ok {
		return s
	}
	return v
}

func Load() (c Config, err error) {
	integer := func(k string, v, min, max int) int {
		n, e := strconv.Atoi(env(k, strconv.Itoa(v)))
		if (e != nil || n < min || n > max) && err == nil {
			err = fmt.Errorf("%s must be %d..%d", k, min, max)
		}
		return n
	}
	duration := func(k string, v time.Duration) time.Duration {
		n, e := time.ParseDuration(env(k, v.String()))
		if (e != nil || n <= 0) && err == nil {
			err = fmt.Errorf("%s must be a positive duration", k)
		}
		return n
	}
	c.Display = env("DISPLAY", ":99")
	c.Game = env("GAME_EXECUTABLE", "chocolate-doom")
	c.IWAD = env("GAME_IWAD", "/usr/share/games/doom/freedoom2.wad")
	c.Width = integer("SCREEN_WIDTH", 426, 320, 1280)
	c.Height = integer("SCREEN_HEIGHT", 240, 200, 720)
	c.Depth = integer("SCREEN_DEPTH", 24, 24, 24)
	c.FPS = integer("VIDEO_FPS", 30, 1, 30)
	c.Bitrate = env("VIDEO_BITRATE", "500k")
	c.PublicIP = env("PUBLIC_IP", "")
	c.Origin = env("ALLOWED_ORIGIN", "")
	c.SessionID = env("SESSION_ID", "")
	c.SignalingPort = integer("SIGNALING_PORT", 7000, 1, 65535)
	c.WebRTCPort = integer("WEBRTC_PORT", 8000, 1, 65535)
	c.Init = duration("INIT_TIMEOUT", 60*time.Second)
	c.Idle = duration("IDLE_TIMEOUT", 120*time.Second)
	c.Signal = duration("SIGNALING_TIMEOUT", 15*time.Second)
	c.Connect = duration("CONNECT_TIMEOUT", 20*time.Second)
	c.Heartbeat = duration("INPUT_HEARTBEAT", time.Second)
	c.Focus = duration("FOCUS_TIMEOUT", 30*time.Second)
	c.Grace = duration("SHUTDOWN_GRACE", 5*time.Second)
	c.Health = duration("HEALTH_INTERVAL", 2*time.Second)
	if err != nil {
		return
	}
	c.Standalone, err = strconv.ParseBool(env("STANDALONE", "false"))
	if err != nil {
		return
	}
	c.SDKAddress = net.JoinHostPort(env("AGONES_SDK_GRPC_HOST", "localhost"), env("AGONES_SDK_GRPC_PORT", "9357"))
	c.TLSCert = env("TLS_CERT_FILE", "")
	c.TLSKey = env("TLS_KEY_FILE", "")
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be supplied together")
	}
	if !regexp.MustCompile(`^:[0-9]+$`).MatchString(c.Display) || c.Width%2 != 0 || c.Height%2 != 0 {
		return c, fmt.Errorf("invalid local display or odd screen dimensions")
	}
	if !regexp.MustCompile(`^[1-9][0-9]{0,3}k$`).MatchString(c.Bitrate) || !strings.EqualFold(env("VIDEO_CODEC", "H264"), "H264") {
		return c, fmt.Errorf("expected H264 and VIDEO_BITRATE of 1k..9999k")
	}
	if c.PublicIP != "" && net.ParseIP(c.PublicIP).To4() == nil {
		return c, fmt.Errorf("PUBLIC_IP must be IPv4")
	}
	if c.Standalone && c.SessionID == "" {
		return c, fmt.Errorf("standalone mode needs SESSION_ID")
	}
	c.PublicKey, err = base64.StdEncoding.DecodeString(env("SESSION_PUBLIC_KEY", ""))
	if err != nil || len(c.PublicKey) != ed25519.PublicKeySize {
		return c, fmt.Errorf("SESSION_PUBLIC_KEY must contain a base64 Ed25519 public key")
	}
	return c, nil
}
