package lib

import (
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"context"
	"fmt"
	"log"
	"time"
)

func TsClient(order *pb.LibraAuditOrder) ([]*pb.Record, error) {

	c := pb.NewJunoClient(model.Conn)
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
	c := pb.NewJunoClient(model.Conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	r, err := c.OrderDDLExec(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
	}
	fmt.Println(r.Message)
}

func ExDMLClient(order *pb.LibraAuditOrder) {

	// Set up a connection to the server.
	c := pb.NewJunoClient(model.Conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	r, err := c.OrderDMLExec(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
	}
	fmt.Println(r.Message)
}

func ExAutoTask(order *pb.LibraAuditOrder) bool {

	c := pb.NewJunoClient(model.Conn)
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

func ExQuery(order *pb.LibraAuditOrder) *pb.InsulateWordList {

	c := pb.NewJunoClient(model.Conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer func() {
		cancel()
	}()
	r, err := c.Query(ctx, order)
	if err != nil {
		log.Printf("could not connect: %v", err)
	}
	return r
}

func ExKillOsc(order *pb.LibraAuditOrder) *pb.Isok {

	c := pb.NewJunoClient(model.Conn)
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
