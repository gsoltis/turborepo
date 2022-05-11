package daemon

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

/**
Static
#cgo LDFLAGS: ../daemon/target/release/libturbocore.a -ldl
Shared
#cgo LDFLAGS: -L../../../daemon/turbocore/target/release -lturbocore
*/

/*
#cgo LDFLAGS: ../daemon/turbocore/target/release/libturbocore.a -ldl
#include "../../../daemon/turbocore/turbocore.h"
#include "stdlib.h"
*/
import "C"

func Run() int {
	fmt.Println("running")
	return int(C.run())
	// err := runTurboServer()
	// if err != nil {
	// 	fmt.Printf("server error: %v\n", err)
	// 	return 1
	// }
	// return 0
}

// type turboServer struct {
// 	UnimplementedTurboServer
// 	gh unsafe.Pointer
// }

// func (ts *turboServer) GetGlobalHash(ctx context.Context, req *GlobalHashRequest) (*GlobalHashReply, error) {
// 	fmt.Printf("Calling get hash w/ %v", ts.gh)
// 	chash := C.get_global_hash(ts.gh)
// 	hash := C.GoString(chash)
// 	C.deallocate_global_hash(chash)
// 	return &GlobalHashReply{Hash: []byte(hash)}, nil
// }

// func runTurboServer() error {
// 	lis, err := net.Listen("tcp", "localhost:5555")
// 	if err != nil {
// 		return err
// 	}
// 	s := grpc.NewServer()
// 	ptr := C.alloc_hasher()
// 	fmt.Printf("allocating a hasher %v\n", ptr)
// 	server := &turboServer{gh: ptr}
// 	RegisterTurboServer(s, server)
// 	if err := s.Serve(lis); err != nil {
// 		return err
// 	}
// 	return nil
// }

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

/**
// // cg/o LDFLAGS: ./lib/turbo/target/release/libturbo.a -ldl
// #cgo LDFLAGS: ../../../../turbo-tooling/target/release/libgit_hash_globs.a -ldl

*/
