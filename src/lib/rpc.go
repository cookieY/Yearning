package lib

import (
	"Yearning-go/src/model"
	"log"
	"net/rpc"
)

func NewRpc() *rpc.Client {
	client, err := rpc.DialHTTP("tcp", model.C.General.RpcAddr)
	if err != nil {
		log.Println(err)
	}
	return client
}
