// Package platform talks only to the local Agones sidecar using its Go gRPC SDK.
package platform

import (
	"context"
	"fmt"
	"net"
	"time"

	"agones.dev/agones/pkg/sdk"
	"github.com/CyberBlock-SPDA/game-streamer/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const SessionAnnotation = "cloudgaming.spda/session-id"

type Platform struct {
	conn         *grpc.ClientConn
	client       sdk.SDKClient
	standaloneID string
}

func Open(c config.Config) (*Platform, error) {
	p := &Platform{standaloneID: c.SessionID}
	if c.Standalone {
		return p, nil
	}
	conn, err := grpc.NewClient(c.SDKAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	p.conn = conn
	p.client = sdk.NewSDKClient(conn)
	return p, nil
}

func (p *Platform) Configure(ctx context.Context, c *config.Config) error {
	if p.client == nil {
		return nil
	}
	gs, err := p.client.GetGameServer(ctx, &sdk.Empty{}, grpc.WaitForReady(true))
	if err != nil {
		return err
	}
	status := gs.GetStatus()
	if c.PublicIP == "" {
		c.PublicIP = status.GetAddress()
	}
	if net.ParseIP(c.PublicIP).To4() == nil {
		return fmt.Errorf("GameServer needs a reachable IPv4 address")
	}
	var tcp, udp int
	for _, port := range status.GetPorts() {
		switch port.Name {
		case "signaling":
			tcp = int(port.Port)
		case "webrtc":
			udp = int(port.Port)
		}
	}
	if tcp < 1 || udp < 1 || tcp > 65535 || udp > 65535 {
		return fmt.Errorf("GameServer requires named signaling and webrtc passthrough ports")
	}
	c.SignalingPort = tcp
	c.WebRTCPort = udp
	return nil
}

func (p *Platform) Session(ctx context.Context) (string, error) {
	if p.client == nil {
		return p.standaloneID, nil
	}
	gs, err := p.client.GetGameServer(ctx, &sdk.Empty{})
	if err != nil {
		return "", err
	}
	if gs.GetStatus().GetState() != "Allocated" {
		return "", nil
	}
	return gs.GetObjectMeta().GetAnnotations()[SessionAnnotation], nil
}

func (p *Platform) Ready(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	_, err := p.client.Ready(ctx, &sdk.Empty{})
	return err
}

func (p *Platform) Health(ctx context.Context, ticks <-chan time.Time) error {
	if p.client == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	stream, err := p.client.Health(ctx)
	if err != nil {
		return err
	}
	for {
		if err := stream.Send(&sdk.Empty{}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticks:
		}
	}
}

func (p *Platform) Shutdown(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	defer p.conn.Close()
	_, err := p.client.Shutdown(ctx, &sdk.Empty{})
	return err
}
