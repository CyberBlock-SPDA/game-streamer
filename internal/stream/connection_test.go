package stream

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/auth"
	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
	"github.com/CyberBlock-SPDA/game-streamer/internal/input"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type observedInput struct{ keyDown atomic.Bool }

func (d *observedInput) Set(k string, down bool) error {
	if k == "Space" {
		d.keyDown.Store(down)
	}
	return nil
}

func (*observedInput) Move(int, int) error { return nil }

type endpoint struct{ url, token, origin string }

func (e endpoint) dial(t *testing.T, ctx context.Context) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	if e.origin != "" {
		h.Set("Origin", e.origin)
	}
	w, _, err := websocket.Dial(ctx, e.url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.CloseNow() })
	return w
}

type reply struct {
	Type, SDP, Code, SessionID string
	Candidate                  webrtc.ICECandidateInit
}

func readReply(t *testing.T, ctx context.Context, w *websocket.Conn) reply {
	t.Helper()
	var m reply
	if err := wsjson.Read(ctx, w, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func write(t *testing.T, ctx context.Context, w *websocket.Conn, m any) {
	t.Helper()
	if err := wsjson.Write(ctx, w, m); err != nil {
		t.Fatal(err)
	}
}

func play(t *testing.T, e endpoint, check func(*webrtc.PeerConnection, uint32)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	bad := e.dial(t, ctx)
	write(t, ctx, bad, map[string]string{"type": "offer", "sdp": "premature"})
	if m := readReply(t, ctx, bad); m.Code != "AUTHENTICATION_FAILED" {
		t.Fatalf("premature SDP: %+v", m)
	}
	ws := e.dial(t, ctx)
	write(t, ctx, ws, map[string]string{"type": "authenticate", "token": e.token})
	if m := readReply(t, ctx, ws); m.Type != "authenticated" || m.SessionID == "" {
		t.Fatalf("authentication: %+v", m)
	}
	duplicate := e.dial(t, ctx)
	if m := readReply(t, ctx, duplicate); m.Code != "SESSION_ALREADY_CONNECTED" {
		t.Fatalf("second connection: %+v", m)
	}
	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	if err != nil {
		t.Fatal(err)
	}
	ordered := false
	max := uint16(0)
	dc, err := pc.CreateDataChannel("game-input", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &max})
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	packets := make(chan uint32, 16)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Codec().MimeType != webrtc.MimeTypeH264 {
			return
		}
		for {
			p, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if p.Marker {
				select {
				case packets <- p.SSRC:
				default:
				}
			}
		}
	})
	local := make(chan *webrtc.ICECandidate, 32)
	pc.OnICECandidate(func(c *webrtc.ICECandidate) { local <- c })
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	// Deliberately trickle the first candidate ahead of the offer to exercise queuing.
	select {
	case c := <-local:
		if c == nil {
			t.Fatal("no local candidate")
		}
		write(t, ctx, ws, map[string]any{"type": "ice-candidate", "candidate": c.ToJSON()})
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	write(t, ctx, ws, map[string]string{"type": "offer", "sdp": offer.SDP})
	for end := false; !end; {
		select {
		case c := <-local:
			if c == nil {
				write(t, ctx, ws, map[string]string{"type": "ice-complete"})
				end = true
			} else {
				write(t, ctx, ws, map[string]any{"type": "ice-candidate", "candidate": c.ToJSON()})
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	m := readReply(t, ctx, ws)
	if m.Type != "answer" {
		t.Fatalf("expected answer, got %s %s", m.Type, m.Code)
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: m.SDP}); err != nil {
		t.Fatal(err)
	}
	for {
		m = readReply(t, ctx, ws)
		if m.Type == "ice-complete" {
			if err = pc.AddICECandidate(webrtc.ICECandidateInit{}); err != nil {
				t.Fatal(err)
			}
			break
		}
		if m.Type != "ice-candidate" {
			t.Fatal(m.Type)
		}
		if err = pc.AddICECandidate(m.Candidate); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("input channel:", ctx.Err())
	}
	if err = ws.Close(websocket.StatusNormalClosure, "signaling complete"); err != nil {
		t.Fatal(err)
	}
	if err = dc.SendText(`{"type":"key","code":"Space","action":"down","seq":0}`); err != nil {
		t.Fatal(err)
	}
	var ssrc uint32
	for i := 0; i < 6; i++ {
		select {
		case ssrc = <-packets:
		case <-ctx.Done():
			t.Fatal("H264 over SRTP:", ctx.Err())
		}
	}
	check(pc, ssrc)
	if err = dc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerSession(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	id := "allocated-game"
	c := config.Config{PublicKey: pub, Signal: time.Second, Connect: 5 * time.Second, Idle: time.Minute, FPS: 30, Heartbeat: time.Second}
	device := &observedInput{}
	var requests atomic.Int32
	s, err := New(ctx, c, input.New(device, c.Heartbeat), func(context.Context) (string, error) { return id, nil }, func() { requests.Add(1) }, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Ready()
	hs := httptest.NewServer(s)
	defer hs.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(time.Second / 30)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = s.Sample([]byte{0, 0, 1, 0x65, 1, 2, 3})
			}
		}
	}()
	mint := func(id string) string {
		cl := auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "cloud-gaming-backend", Audience: jwt.ClaimStrings{"game-streamer"}, Subject: id, ID: "test", IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}, GameID: "doom-1993"}
		token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, cl).SignedString(priv)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	e := endpoint{url: "ws" + strings.TrimPrefix(hs.URL, "http"), token: mint(id)}
	wrong := e.dial(t, ctx)
	write(t, ctx, wrong, map[string]string{"type": "authenticate", "token": mint("other-pod")})
	if readReply(t, ctx, wrong).Code != "AUTHENTICATION_FAILED" {
		t.Fatal("accepted another allocation")
	}
	play(t, e, func(pc *webrtc.PeerConnection, ssrc uint32) {
		if !device.keyDown.Load() || s.Status() != "PLAYING" {
			t.Fatal("missing input or PLAYING state")
		}
		before := requests.Load()
		err := pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}, &rtcp.FullIntraRequest{MediaSSRC: ssrc, FIR: []rtcp.FIREntry{{SSRC: ssrc, SequenceNumber: 1}}}})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for requests.Load() < before+2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if requests.Load() < before+2 {
			t.Fatal("encoder missed PLI/FIR")
		}
	})
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session survived disconnect")
	}
}

func TestAllocatedGameServer(t *testing.T) {
	url := os.Getenv("LIVE_WS")
	if url == "" {
		t.Skip("set LIVE_WS, SESSION_TOKEN, and ALLOWED_ORIGIN to test one allocated server")
	}
	token := os.Getenv("SESSION_TOKEN")
	if token == "" {
		t.Fatal("missing SESSION_TOKEN")
	}
	play(t, endpoint{url, token, os.Getenv("ALLOWED_ORIGIN")}, func(pc *webrtc.PeerConnection, ssrc uint32) {
		if err := pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}}); err != nil {
			t.Fatal(err)
		}
		t.Logf("received H.264 RTP and opened game-input after authenticating at %s", url)
	})
}
