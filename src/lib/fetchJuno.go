package lib

import (
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"log"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var (
	globalGRPCconns unsafe.Pointer
	lock            sync.Mutex
)

func FetchGRPCConn() (*grpc.ClientConn, error) {

	if atomic.LoadPointer(&globalGRPCconns) != nil {
		if (*grpc.ClientConn)(globalGRPCconns).GetState() == connectivity.Connecting {
			return (*grpc.ClientConn)(globalGRPCconns), nil
		}
	}

	lock.Lock()

	defer lock.Unlock()

	cli, err := newGrpcConn()
	cli.Target()

	if err != nil {
		return nil, err
	}

	atomic.StorePointer(&globalGRPCconns, unsafe.Pointer(cli))

	return cli, nil
}

func newGrpcConn() (*grpc.ClientConn, error) {
	conn, err := grpc.Dial(
		model.Grpc,
		grpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func TsClient(order *pb.LibraAuditOrder) ([]*pb.Record, error) {

	conn, err := FetchGRPCConn()

	if err != nil {
		return nil, err
	}

	c := pb.NewJunoClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	r, err := c.OrderDeal(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
		return []*pb.Record{}, err
	}
	defer func() {
		cancel()
	}()

	return r.Record, nil
}

func ExDDLClient(order *pb.LibraAuditOrder) {
	// Set up a connection to the server.

	conn, err := FetchGRPCConn()

	if err != nil {
		log.Println(err.Error())
		return
	}

	c := pb.NewJunoClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	_, err = c.OrderDDLExec(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
		MessagePush(order.WorkId, 4, "")
	}
	MessagePush(order.WorkId, 1, "")
}

func ExDMLClient(order *pb.LibraAuditOrder) {

	conn, err := FetchGRPCConn()

	if err != nil {
		log.Println(err.Error())
		return
	}

	// Set up a connection to the server.
	c := pb.NewJunoClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	_, err = c.OrderDMLExec(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
		MessagePush(order.WorkId, 4, "")
	}
	MessagePush(order.WorkId, 1, "")
}

func ExAutoTask(order *pb.LibraAuditOrder) bool {

	conn, err := FetchGRPCConn()

	if err != nil {
		log.Println(err.Error())
		return false
	}

	c := pb.NewJunoClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)

	defer func() {
		cancel()
	}()
	r, err := c.AutoTask(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
	}
	return r.Ok
}

func ExQuery(order *pb.LibraAuditOrder) (*pb.InsulateWordList, error) {
	conn, err := FetchGRPCConn()

	if err != nil {
		log.Println(err.Error())
	}
	c := pb.NewJunoClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	r, err := c.Query(ctx, order)
	if err != nil {
		return r, err
	}
	return r, nil
}

func ExKillOsc(order *pb.LibraAuditOrder) *pb.Isok {
	conn, err := FetchGRPCConn()

	if err != nil {
		log.Println(err.Error())
	}
	c := pb.NewJunoClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	r, err := c.KillOsc(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
	}
	return r
}

func OverrideConfig(order *pb.LibraAuditOrder) *pb.Isok {
	conn, err := FetchGRPCConn()

	if err != nil {
		log.Println(err.Error())
	}
	c := pb.NewJunoClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	r, err := c.OverrideConfig(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
	}
	return r
}
