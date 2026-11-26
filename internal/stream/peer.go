package stream

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
	"github.com/CyberBlock-SPDA/game-streamer/internal/input"
	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type Server struct {
	ctx       context.Context
	fail      context.CancelCauseFunc
	life      lifecycle
	api       *webrtc.API
	mux       *ice.UDPMuxDefault
	track     *webrtc.TrackLocalStaticSample
	controls  *input.Controller
	sessionID func(context.Context) (string, error)
	keyframe  func()
	peer      atomic.Pointer[webrtc.PeerConnection]
	authSlots chan struct{}
}

func New(ctx context.Context, c config.Config, controls *input.Controller, id func(context.Context) (string, error), keyframe func(), fail context.CancelCauseFunc) (*Server, error) {
	engine := &webrtc.MediaEngine{}
	codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: codec, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(engine, reg); err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(codec, "screen", "doom")
	if err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(c.PublicIP == "" || c.PublicIP == "127.0.0.1")
	if c.PublicIP != "" {
		if err = se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{External: []string{c.PublicIP}, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteReplace}); err != nil {
			return nil, err
		}
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{Port: c.WebRTCPort})
	if err != nil {
		return nil, err
	}
	mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: udp})
	se.SetICEUDPMux(mux)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se))
	return &Server{ctx: ctx, fail: fail, life: lifecycle{c: c, phase: "INITIALIZING"}, api: api, mux: mux, track: track, controls: controls, sessionID: id, keyframe: keyframe, authSlots: make(chan struct{}, 8)}, nil
}

func (s *Server) Ready()         { s.life.ready() }
func (s *Server) Status() string { return s.life.status() }
func (s *Server) Sample(data []byte) error {
	return s.track.WriteSample(media.Sample{Data: data, Duration: time.Second / time.Duration(s.life.c.FPS)})
}

func (s *Server) Close() {
	s.life.mu.Lock()
	s.life.transition("SHUTTING_DOWN", 0)
	p := s.peer.Load()
	s.life.mu.Unlock()
	_ = s.controls.Close()
	if p != nil {
		_ = p.Close()
	}
	_ = s.mux.Close()
}

func (s *Server) createPeer() (*webrtc.PeerConnection, error) {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	if s.ctx.Err() != nil || s.life.phase != "SIGNALING" {
		return nil, fmt.Errorf("session closing")
	}
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	s.peer.Store(pc)
	sender, err := pc.AddTrack(s.track)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			pkts, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range pkts {
				switch p.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					s.keyframe()
				}
			}
		}
	}()
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.life.connected(false)
			s.keyframe()
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.fail(fmt.Errorf("%w: WebRTC %s", ErrEnded, state))
		}
	})
	var gotInput atomic.Bool
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		retransmits := dc.MaxRetransmits()
		if dc.Label() != "game-input" || dc.Ordered() || retransmits == nil || *retransmits != 0 || dc.MaxPacketLifeTime() != nil || !gotInput.CompareAndSwap(false, true) {
			_ = dc.Close()
			return
		}
		dc.OnOpen(func() { s.life.connected(true) })
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if m.IsString && pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
				if err := s.controls.Apply(m.Data, time.Now()); err != nil {
					s.fail(err)
				}
			}
		})
		dc.OnClose(func() { _ = s.controls.Close(); s.fail(fmt.Errorf("%w: input channel closed", ErrEnded)) })
	})
	return pc, nil
}
