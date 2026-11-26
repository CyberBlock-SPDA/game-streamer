// Command demo runs one local Docker game session and serves a loopback-only browser client.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

//go:embed play.html
var page string

func run(ctx context.Context) error {
	image := flag.String("image", "game-streamer:local", "agent Docker image")
	port := flag.Int("port", 7070, "loopback demo HTTP port")
	tcp := flag.Int("signaling", 7000, "loopback TCP signaling port")
	udp := flag.Int("webrtc", 8000, "loopback UDP media port")
	flag.Parse()
	ui, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		return err
	}
	defer ui.Close()
	origin := "http://" + ui.Addr().String()
	ws := fmt.Sprintf("ws://127.0.0.1:%d/ws", *tcp)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("local-%d", time.Now().UnixNano())
	tmpl := template.Must(template.New("demo").Parse(page))
	srv := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/" || r.Host != ui.Addr().String() {
			http.NotFound(w, r)
			return
		}
		now := time.Now()
		cl := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "cloud-gaming-backend", Audience: jwt.ClaimStrings{"game-streamer"}, Subject: id, ID: id, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute))}, GameID: "doom-1993"}
		token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, cl).SignedString(priv)
		if err != nil {
			http.Error(w, "token unavailable", 500)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		if err = tmpl.Execute(w, map[string]string{"WS": ws, "Token": token}); err != nil {
			log.Print("demo page interrupted")
		}
	})}
	defer srv.Close()
	name := "game-streamer-" + id
	docker := exec.Command("docker", "run", "--rm", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d/tcp", *tcp, *tcp), "-p", fmt.Sprintf("127.0.0.1:%d:%d/udp", *udp, *udp),
		"-e", "STANDALONE=true", "-e", "PUBLIC_IP=127.0.0.1", "-e", "ALLOWED_ORIGIN="+origin, "-e", "SESSION_ID="+id,
		"-e", "SESSION_PUBLIC_KEY="+base64.StdEncoding.EncodeToString(pub), "-e", fmt.Sprintf("SIGNALING_PORT=%d", *tcp), "-e", fmt.Sprintf("WEBRTC_PORT=%d", *udp), *image)
	docker.Stdout = os.Stdout
	docker.Stderr = os.Stderr
	if err = docker.Start(); err != nil {
		return err
	}
	events := make(chan error, 2)
	go func() { events <- docker.Wait() }()
	go func() { events <- srv.Serve(ui) }()
	log.Printf("Open %s and click Play after the agent reports READY", origin)
	select {
	case err = <-events:
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_ = exec.CommandContext(stop, "docker", "stop", "-t", "8", name).Run()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
