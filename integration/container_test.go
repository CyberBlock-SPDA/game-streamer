// Package integration exercises the built Linux image when RUN_DOCKER_TESTS=1.
package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/game"
)

func image(t *testing.T) string {
	t.Helper()
	if os.Getenv("RUN_DOCKER_TESTS") != "1" {
		t.Skip("build the image and set RUN_DOCKER_TESTS=1")
	}
	if name := os.Getenv("GAME_IMAGE"); name != "" {
		return name
	}
	return "game-streamer:local"
}
func docker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, out)
	}
	return strings.TrimSpace(string(out))
}
func TestContainerLifecycle(t *testing.T) {
	name := image(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, env, action, exit, reason string }{
		{"missing IWAD", "GAME_IWAD=/missing.wad", "", "1", "/missing.wad"},
		{"Doom crash", "", "kill -KILL \"$(xdotool getwindowfocus getwindowpid)\"", "1", "chocolate-doom"},
		{"SIGTERM", "", "stop", "0", "session finished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			defer cancel()
			args := []string{"run", "-d", "-e", "STANDALONE=true", "-e", "SESSION_ID=smoke", "-e", "SESSION_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(pub)}
			if tc.env != "" {
				args = append(args, "-e", tc.env)
			}
			id := docker(t, ctx, append(args, name)...)
			defer func() {
				cleanup, end := context.WithTimeout(context.Background(), 8*time.Second)
				defer end()
				_ = exec.CommandContext(cleanup, "docker", "rm", "-f", id).Run()
			}()
			if tc.action != "" {
				for !strings.Contains(docker(t, ctx, "logs", id), "listeners ready") {
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(100 * time.Millisecond):
					}
				}
				if uid := docker(t, ctx, "exec", id, "id", "-u"); uid != "10001" {
					t.Fatalf("unexpected UID %s", uid)
				}
				if tc.action == "stop" {
					docker(t, ctx, "stop", "-t", "8", id)
				} else {
					docker(t, ctx, "exec", id, "sh", "-ec", tc.action)
				}
			}
			if exit := docker(t, ctx, "wait", id); exit != tc.exit {
				t.Fatalf("exit %s: %s", exit, docker(t, ctx, "logs", id))
			}
			logs := docker(t, ctx, "logs", id)
			if !strings.Contains(logs, tc.reason) || !strings.Contains(logs, "TERMINATED") {
				t.Fatal(logs)
			}
			if tc.action == "" && strings.Contains(logs, "state=READY") {
				t.Fatal("initialization failure reported Ready")
			}
		})
	}
}

func TestEncoderForcesNextIDR(t *testing.T) {
	img := image(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	name := fmt.Sprintf("spda-idr-%d", time.Now().UnixNano())
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--name", name, "--entrypoint", "game-encoder", img, "426", "240", "30", "500k")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	frame := func() map[byte]bool {
		t.Helper()
		if _, err = in.Write(make([]byte, 426*240*3/2)); err != nil {
			t.Fatal(err)
		}
		unit, err := game.NextUnit(out)
		if err != nil {
			t.Fatal(err)
		}
		types := map[byte]bool{}
		for _, nal := range bytes.Split(unit, []byte{0, 0, 1}) {
			if len(nal) > 0 {
				types[nal[0]&31] = true
			}
		}
		return types
	}
	first := frame()
	delta := frame()
	docker(t, ctx, "kill", "--signal=USR1", name)
	forced := frame()
	if !first[5] || delta[5] || !delta[1] || !forced[5] || !forced[7] || !forced[8] {
		t.Fatalf("initial %v delta %v forced %v", first, delta, forced)
	}
}
