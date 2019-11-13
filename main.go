// Copyright 2019 HenryYee.
//
// Licensed under the AGPL, Version 3.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.gnu.org/licenses/agpl-3.0.en.html
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"Yearning-go/src/model"
	"Yearning-go/src/pool"
	"Yearning-go/src/service"
	"flag"
	"fmt"
	"log"
	"os"
)

var (
	h   bool
	s   bool
	p   string
	m   bool
	b   string
	x   bool
	c   string
)

func usage() {
	_, err := fmt.Fprintf(os.Stderr, `version: Yearning/2.1.6 author: HenryYee
Usage: Yearning [-m migrate] [-p port] [-s start] [-b web-bind] [-h help] [-c config file]

Options:
 -s  启动Yearning
 -m  数据初始化(第一次安装时执行)
 -p  端口
 -b  钉钉/邮件推送时显示的平台地址
 -x  表结构修复,升级时可以操作。如出现错误可直接忽略。
 -h  帮助
 -c  配置文件路径
`)
	if err != nil {
		panic(err.Error())
	}
}

func init() {
	flag.BoolVar(&s, "s", false, "启动Yearning")
	flag.BoolVar(&m, "m", false, "数据初始化(第一次安装时执行)")
	flag.StringVar(&p, "p", "8000", "Yearning端口")
	flag.BoolVar(&h, "h", false, "帮助")
	flag.BoolVar(&x, "x", false, "表结构修复")
	flag.StringVar(&b, "b", "127.0.0.1", "钉钉/邮件推送时显示的平台地址")
	flag.StringVar(&c, "c", "conf.toml", "配置文件路径")
	flag.Usage = usage
	log.SetPrefix("Yearning_error: ")
	log.SetFlags(log.Ldate | log.Lmicroseconds | log.Llongfile)
}

//var Version = "v2.0.2"

func main() {
	defer pool.P.Close()
	flag.Parse()
	if h {
		flag.Usage()
	}

	if m {
		model.DbInit(c)
		service.Migrate()
	}

	if s {
		model.DbInit(c)
		err := pool.InitGrpcpool()
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
		service.UpdateSoft()
		service.StartYearning(p, b)
	}

	if x {
		model.DbInit(c)
		service.DelCol()
	}
}
