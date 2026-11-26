package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/auth"
	"github.com/CyberBlock-SPDA/game-streamer/internal/wire"
	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pion/webrtc/v4"
)

type socket struct {
	*websocket.Conn
	ctx context.Context
}

func (w socket) send(msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	return w.Write(ctx, websocket.MessageText, raw)
}

func (w socket) reject(code, msg string) {
	_ = w.send(map[string]string{"type": "error", "code": code, "message": msg})
}

func (w socket) read(ctx context.Context) ([]byte, error) {
	typ, raw, err := w.Read(ctx)
	if err == nil && typ != websocket.MessageText {
		return nil, errors.New("expected text")
	}
	return raw, err
}

func (s *Server) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if s.ctx.Err() != nil {
		http.Error(rw, "closing", 503)
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		http.Error(rw, "busy", 429)
		return
	}
	opts := &websocket.AcceptOptions{}
	if origin := s.life.c.Origin; origin != "" {
		if r.Header.Get("Origin") != origin {
			http.Error(rw, "invalid origin", 403)
			return
		}
		opts.InsecureSkipVerify = true // Exact scheme, host and port checked above.
	}
	conn, err := websocket.Accept(rw, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128 << 10)
	w := socket{conn, s.ctx}
	if s.Status() != "READY" {
		w.reject("SESSION_ALREADY_CONNECTED", "Session unavailable")
		return
	}
	if !s.authenticate(w) {
		return
	}
	err = s.negotiate(w)
	if err != nil && s.ctx.Err() == nil {
		s.fail(fmt.Errorf("%w: signaling ended during negotiation", ErrEnded))
	}
}

func (s *Server) authenticate(w socket) bool {
	ctx, cancel := context.WithTimeout(s.ctx, s.life.c.Signal)
	defer cancel()
	raw, err := w.read(ctx)
	if err != nil {
		return false
	}
	obj, err := wire.Object(raw, "type", "token")
	var token string
	if err != nil || wire.Type(raw) != "authenticate" || json.Unmarshal(obj["token"], &token) != nil {
		w.reject("AUTHENTICATION_FAILED", "Expected authentication")
		return false
	}
	id, err := auth.Verify(token, s.life.c.PublicKey)
	if err != nil {
		code := "AUTHENTICATION_FAILED"
		if errors.Is(err, jwt.ErrTokenExpired) && !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			code = "SESSION_EXPIRED"
		}
		w.reject(code, "Invalid or expired session token")
		return false
	}
	expected, err := s.sessionID(ctx)
	if err != nil {
		w.reject("SERVER_ERROR", "Allocation lookup unavailable")
		return false
	}
	if expected == "" || expected != id {
		w.reject("AUTHENTICATION_FAILED", "Invalid session token")
		return false
	}
	if !s.life.claim() {
		w.reject("SESSION_ALREADY_CONNECTED", "Session unavailable")
		return false
	}
	if err = w.send(map[string]string{"type": "authenticated", "sessionId": id}); err != nil {
		s.fail(fmt.Errorf("%w: client left", ErrEnded))
		return false
	}
	return true
}

// Answer and local candidates share this gate because gathering callbacks can
// run before SetLocalDescription returns. The browser must receive SDP first.
type outgoing struct {
	sync.Mutex
	ws                 socket
	answered, complete bool
	pending            []localICE
}

type localICE struct {
	message any
	end     bool
}

func (o *outgoing) candidate(c *webrtc.ICECandidate) {
	o.Lock()
	defer o.Unlock()
	var msg any = map[string]string{"type": "ice-complete"}
	if c != nil {
		msg = map[string]any{"type": "ice-candidate", "candidate": c.ToJSON()}
	}
	if !o.answered {
		o.pending = append(o.pending, localICE{message: msg, end: c == nil})
		return
	}
	if o.ws.send(msg) == nil && c == nil {
		o.complete = true
	}
}

func (o *outgoing) answer(sdp string) error {
	o.Lock()
	defer o.Unlock()
	if err := o.ws.send(map[string]string{"type": "answer", "sdp": sdp}); err != nil {
		return err
	}
	o.answered = true
	for _, m := range o.pending {
		if err := o.ws.send(m.message); err != nil {
			return err
		}
		if m.end {
			o.complete = true
		}
	}
	o.pending = nil
	return nil
}

func (o *outgoing) done() bool { o.Lock(); defer o.Unlock(); return o.complete }
func (s *Server) negotiate(w socket) error {
	out := &outgoing{ws: w}
	var pc *webrtc.PeerConnection
	var pending []webrtc.ICECandidateInit
	remoteEnd := false
	invalid := func(code string) error { w.reject(code, "Invalid signaling message"); return errors.New(code) }
	for {
		raw, err := w.read(s.ctx)
		if err != nil {
			// The browser can observe DTLS connected before the local Pion callback.
			// Once both ICE generations are complete the connect deadline owns cleanup.
			if pc != nil && remoteEnd && out.done() {
				return nil
			}
			return err
		}
		typ := wire.Type(raw)
		switch typ {
		case "offer":
			obj, err := wire.Object(raw, "type", "sdp")
			var offer string
			if pc != nil || err != nil || json.Unmarshal(obj["sdp"], &offer) != nil || offer == "" {
				return invalid("INVALID_SDP")
			}
			pc, err = s.createPeer()
			if err != nil {
				return invalid("WEBRTC_NEGOTIATION_FAILED")
			}
			s.life.connecting()
			pc.OnICECandidate(out.candidate)
			if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
				return invalid("INVALID_SDP")
			}
			for _, c := range pending {
				if err = pc.AddICECandidate(c); err != nil {
					return invalid("INVALID_MESSAGE")
				}
			}
			pending = nil
			a, err := pc.CreateAnswer(nil)
			if err != nil {
				return invalid("WEBRTC_NEGOTIATION_FAILED")
			}
			if !playable(a) {
				return invalid("WEBRTC_NEGOTIATION_FAILED")
			}
			if err = pc.SetLocalDescription(a); err != nil {
				return invalid("WEBRTC_NEGOTIATION_FAILED")
			}
			if err = out.answer(a.SDP); err != nil {
				return err
			}
		case "ice-candidate", "ice-complete":
			if remoteEnd {
				return invalid("INVALID_MESSAGE")
			}
			var c webrtc.ICECandidateInit
			if typ == "ice-complete" {
				if _, err := wire.Object(raw, "type"); err != nil {
					return invalid("INVALID_MESSAGE")
				}
				remoteEnd = true
			} else {
				obj, err := wire.Object(raw, "type", "candidate")
				if err != nil || wire.Strict(obj["candidate"], &c) != nil || c.Candidate == "" {
					return invalid("INVALID_MESSAGE")
				}
			}
			if pc == nil {
				if len(pending) >= 64 {
					return invalid("INVALID_MESSAGE")
				}
				pending = append(pending, c)
			} else if err := pc.AddICECandidate(c); err != nil {
				return invalid("INVALID_MESSAGE")
			}
		default:
			return invalid("INVALID_MESSAGE")
		}
	}
}

func playable(a webrtc.SessionDescription) bool {
	parsed, err := a.Unmarshal()
	if err != nil {
		return false
	}
	video, data := false, false
	for _, m := range parsed.MediaDescriptions {
		if m.MediaName.Port.Value == 0 {
			continue
		}
		switch m.MediaName.Media {
		case "video":
			_, video = m.Attribute("sendonly")
			if !video {
				_, video = m.Attribute("sendrecv")
			}
		case "application":
			_, data = m.Attribute("sctp-port")
		}
	}
	return video && data
}
