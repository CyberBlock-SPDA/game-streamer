# game-streamer

One Go agent runs one playable Doom session. It starts Chocolate Doom with the freely distributable Freedoom IWAD in Xvfb, streams H.264 to a browser through Pion WebRTC, and maps keyboard and mouse events to X11. In a Fleet it authenticates the allocated player and reports readiness, health and shutdown to Agones.

## Run the checks

Use Go 1.26.5 or newer and a running Docker engine. Run these commands from the repository root.

```sh
go test -race ./...
go vet ./...
docker build -t game-streamer:local .
RUN_DOCKER_TESTS=1 go test -race -count=1 -v ./integration
go run ./cmd/demo
```

When the agent logs `state=READY`, open **http://127.0.0.1:7070** and click **Play**. The demo starts its own loopback-only Docker container and creates an ephemeral Ed25519 key. It needs TCP 7070 and 7000 and UDP 8000. For occupied ports, run `go run ./cmd/demo -port 7071 -signaling 7001 -webrtc 8001`.

1. Press **Escape** to open the game menu and press it again to resume.
2. Use the **arrow keys** to move and turn. Move the mouse over the video and hold the **left mouse button** to fire. **Space** opens doors.
3. Switch tabs briefly while holding a key, then return. Controls should release. A 30-second loss of focus ends the session.
4. Close the page. The agent exits once it observes the disconnect. Run the demo command again for a fresh game.

In a second terminal, `curl -fsS http://127.0.0.1:7000/healthz` prints `READY` before Play and `PLAYING` during the game. Ctrl+C in the demo terminal stops the container.

The Go tests cover signed tokens, unreliable input, deadlines and a real local Pion connection. The Docker tests exercise the actual Linux binaries, verify non-root execution and force both a game crash and an encoder keyframe. The detailed report includes the network contract, configuration, Fleet example and acceptance results. [Implementation and verification](docs/implementation.md)
