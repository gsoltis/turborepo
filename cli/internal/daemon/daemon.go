package daemon

import (
	context "context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Run() int {
	err := runTurboServer()
	if err != nil {
		fmt.Printf("server error: %v\n", err)
		return 1
	}
	return 0
}

type turboServer struct {
	UnimplementedTurboServer
}

func (ts *turboServer) GetGlobalHash(ctx context.Context, req *GlobalHashRequest) (*GlobalHashReply, error) {
	return &GlobalHashReply{Hash: []byte("florp")}, nil
	//return nil, errors.New("unimplemented")
}

func runTurboServer() error {
	lis, err := net.Listen("tcp", "localhost:5555")
	if err != nil {
		return err
	}
	s := grpc.NewServer()
	server := &turboServer{}
	RegisterTurboServer(s, server)
	if err := s.Serve(lis); err != nil {
		return err
	}
	return nil
}

func RunClient() error {
	creds := insecure.NewCredentials()
	addr := "localhost:5555"
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c := NewTurboClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Hour)
	defer cancel()
	r, err := c.GetGlobalHash(ctx, &GlobalHashRequest{})
	if err != nil {
		return err
	}
	fmt.Printf("Got Hash: %v\n", string(r.Hash))
	return nil
}
