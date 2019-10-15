package main

import (
	"Yearning-go/src/pool"
	"Yearning-go/src/service"
	"flag"
	"fmt"
	"log"
	"os"
)

var (
	h  bool
	s  bool
	p  string
	m  bool
	b  string
	up bool
)

func usage() {
	_, err := fmt.Fprintf(os.Stderr, `version: Yearning/2.0.0
Usage: Yearning [-m migrate] [-p port] [-s start] [-b web-bind] [-h help] [-x update]

Options:
`)
	if err != nil {
		panic(err.Error())
	}

	flag.PrintDefaults()
}

func init() {
	flag.BoolVar(&s, "s", false, "启动Yearning")
	flag.BoolVar(&m, "m", false, "数据初始化(第一次安装时执行)")
	flag.StringVar(&p, "p", "8000", "Yearning端口")
	flag.BoolVar(&h, "h", false, "帮助")
	flag.BoolVar(&up, "x", false, "更新")
	flag.StringVar(&b, "b", "127.0.0.1", "钉钉/邮件推送时显示的平台地址")
	flag.Usage = usage
}

//var Version = "v2.0.2"

func init() {
	log.SetPrefix("Yearning_error: ")
	log.SetFlags(log.Ldate | log.Lmicroseconds | log.Llongfile)
}
func main() {
	defer pool.P.Close()
	flag.Parse()
	if h {
		flag.Usage()
	}

	if m {
		service.Migrate()
	}

	if up {
		service.UpdateSoft()
	}

	if s {
		err := pool.InitGrpcpool()
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
		service.StartYearning(p, b)
	}
}
