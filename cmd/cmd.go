package cmd

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"Yearning-go/src/service"
	"fmt"
	"github.com/gookit/gcli/v3"
	"github.com/gookit/gcli/v3/builtin"
	"net"
)

var RunOpts = struct {
	addr       string
	port       string
	push       string
	config     string
	repair     bool
	resetAdmin bool
}{}

var Migrate = &gcli.Command{
	Name:     "install",
	Desc:     "Yearning安装及数据初始化",
	Examples: `{$binName} {$cmd} --config conf.toml`,
	Config: func(c *gcli.Command) {
		c.StrOpt(&RunOpts.config, "config", "c", "conf.toml", "配置文件路径,默认为conf.toml.如无移动配置文件则无需配置！")
	},
	Func: func(c *gcli.Command, args []string) error {
		model.DBNew(RunOpts.config)
		service.Migrate()
		return nil
	},
}

var Fix = &gcli.Command{
	Name: "migrate",
	Desc: "破坏性版本升级修复",
	Config: func(c *gcli.Command) {
		c.StrOpt(&RunOpts.config, "config", "c", "conf.toml", "配置文件路径,默认为conf.toml.如无移动配置文件则无需配置！")
	},
	Func: func(c *gcli.Command, args []string) error {
		model.DBNew(RunOpts.config)
		service.DelCol()
		service.MargeRuleGroup()
		return nil
	},
}

var Super = &gcli.Command{
	Name: "reset_super",
	Desc: "重置超级管理员密码",
	Config: func(c *gcli.Command) {
		c.StrOpt(&RunOpts.config, "config", "c", "conf.toml", "配置文件路径,默认为conf.toml.如无移动配置文件则无需配置！")
	},
	Func: func(c *gcli.Command, args []string) error {
		model.DBNew(RunOpts.config)
		model.DB().Model(model.CoreAccount{}).Where("username =?", "admin").Updates(&model.CoreAccount{Password: lib.DjangoEncrypt("Yearning_admin", string(lib.GetRandom()))})
		fmt.Println("admin密码已重新设置为:Yearning_admin")
		return nil
	},
}

var RunServer = &gcli.Command{
	Name: "run",
	Desc: "启动Yearning",
	Config: func(c *gcli.Command) {
		c.StrOpt(&RunOpts.addr, "addr", "a", "0.0.0.0", "Yearning启动地址")
		c.StrOpt(&RunOpts.port, "port", "p", "8000", "Yearning启动端口")
		c.StrOpt(&RunOpts.push, "push", "b", "127.0.0.1:8000", "钉钉/邮件推送时显示的平台地址")
		c.StrOpt(&RunOpts.config, "config", "c", "conf.toml", "配置文件路径")
	},
	Examples: `<cyan>{$binName} {$cmd} --port 80 --push "yearning.io" --config ../config.toml</>`,
	Func: func(c *gcli.Command, args []string) error {
		model.DBNew(RunOpts.config)
		service.UpdateData()
		service.StartYearning(net.JoinHostPort(RunOpts.addr, RunOpts.port), RunOpts.push)
		return nil
	},
}

func Command() {
	app := gcli.NewApp()
	app.Version = "3.1.5 Uranus"
	app.Name = "Yearning"
	app.Logo = &gcli.Logo{Text: LOGO, Style: "info"}
	app.Desc = "Yearning Mysql数据审核平台"
	app.Add(Migrate)
	app.Add(RunServer)
	app.Add(Fix)
	app.Add(Super)
	app.Add(builtin.GenAutoComplete())
	app.Run(nil)
}
