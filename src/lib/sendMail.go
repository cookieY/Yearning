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

package lib

import (
	"Yearning-go/src/model"
	"crypto/tls"
	"fmt"
	"github.com/cookieY/yee/logger"
	"gopkg.in/gomail.v2"
	"strings"
)

type UserInfo struct {
	ToUser  string
	User    string
	Pawd    string
	Smtp    string
	PubName string
}

type sendInfo struct {
	ToUser  []model.CoreAccount
	Message model.Message
}

var TemoplateTestMail = `
<html>
<body>
	<div style="text-align:center;">
		<h1>Yearning 3.0</h1>
		<h2>此邮件是测试邮件！</h2>
	</div>
</body>
</html>
`

var TmplRejectMail = `
<html>
<body>
<h1>Yearning 工单驳回通知</h1>
<br><p>工单号: %s</p>
<br><p>发起人: %s</p>
<br><p>地址: <a href="%s">%s</a></p>
<br><p>状态: 驳回</p>
<br><p>驳回说明: %s</p>
</body>
</html>
`

var TmplMail = `
<html>
<body>
<h1>Yearning 工单%s通知</h1>
<br><p>工单号: %s</p>
<br><p>发起人: %s</p>
<br><p>地址: <a href="%s">%s</a></p>
<br><p>状态: %s</p>
</body>
</html>
`

var Tmpl2Mail = `
<html>
<body>
<h1>Yearning 工单%s通知</h1>
<br><p>工单号: %s</p>
<br><p>发起人: %s</p>
<br><p>下一步操作人: %s <p>
<br><p>地址: <a href="%s">%s</a></p>
<br><p>状态: %s</p>
</body>
</html>
`

func SendMail(addr string, mail model.Message, tmpl string) {
	m := gomail.NewMessage()
	m.SetHeader("From", mail.User)
	m.SetHeader("To", addr)
	m.SetHeader("Subject", "Yearning消息推送!")
	m.SetBody("text/html", tmpl)
	d := Dialer(mail)
	if mail.Ssl {
		d.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	}
	// Send the email to Bob, Cora and Dan.
	if err := d.DialAndSend(m); err != nil {
		logger.DefaultLogger.Errorf("send mail:%s", err.Error())
		return
	}
}

func Dialer(mail model.Message) *gomail.Dialer {
	d := gomail.Dialer{
		Host:     mail.Host,
		Port:     mail.Port,
		Username: mail.User,
		Password: mail.Password,
		SSL:      mail.Ssl,
	}
	return &d
}

func MessagePush(workid string, t uint, reject string) {
	var user model.CoreAccount
	var o model.CoreSqlOrder
	var ding, mail string
	model.DB().Select("work_id,username,text,assigned,source").Where("work_id =?", workid).First(&o)
	model.DB().Select("email").Where("username = ?", o.Username).First(&user)
	s := new(sendInfo)
	s.ToUser = []model.CoreAccount{user}
	s.Message = model.GloMessage

	if model.GloOther.Query && t > 6 {
		var op model.CoreQueryOrder
		model.DB().Select("work_id,username,text,assigned").Where("work_id =?", workid).First(&op)
		model.DB().Select("email").Where("username = ?", op.Username).First(&user)
		if t == 7 {
			model.DB().Select("email").Where("username IN (?)", strings.Split(op.Assigned, ",")).Find(&s.ToUser)
			ding = dingMsgTplHandler("已提交", op)
			mail = fmt.Sprintf(TmplMail, "查询申请", op.WorkId, op.Username, model.GloOther.Domain, model.GloOther.Domain, "已提交")
		}
		if t == 8 {
			ding = dingMsgTplHandler("已同意", op)
			mail = fmt.Sprintf(TmplMail, "查询申请", op.WorkId, op.Username, model.GloOther.Domain, model.GloOther.Domain, "已同意")
		}
		if t == 9 {
			ding = dingMsgTplHandler("已驳回", op)
			mail = fmt.Sprintf(TmplMail, "查询申请", op.WorkId, op.Username, model.GloOther.Domain, model.GloOther.Domain, "已驳回")
		}
	} else {
		if t == 0 {
			ding = dingMsgTplHandler("已驳回", o)
			mail = fmt.Sprintf(TmplRejectMail, o.WorkId, o.Username, model.GloOther.Domain, model.GloOther.Domain, reject)
		}

		if t == 1 {
			ding = dingMsgTplHandler("已执行", o)
			mail = fmt.Sprintf(TmplMail, "执行", o.WorkId, o.Username, model.GloOther.Domain, model.GloOther.Domain, "执行成功")
		}

		if t == 2 {
			model.DB().Select("email").Where("username IN (?)", strings.Split(o.Assigned, ",")).Find(&s.ToUser)
			ding = dingMsgTplHandler("已提交", o)
			mail = fmt.Sprintf(TmplMail, "提交", o.WorkId, o.Username, model.GloOther.Domain, model.GloOther.Domain, "已提交")
		}

		if t == 4 {
			ding = dingMsgTplHandler("执行失败", o)
			mail = fmt.Sprintf(TmplMail, "执行", o.WorkId, o.Username, model.GloOther.Domain, model.GloOther.Domain, "执行失败")
		}

		if t == 5 {
			model.DB().Select("email").Where("username IN (?)", strings.Split(o.Assigned, ",")).Find(&s.ToUser)
			ding = dingMsgTplHandler("已转交至下一操作人", o)
			mail = fmt.Sprintf(Tmpl2Mail, "转交", o.WorkId, o.Username, o.Assigned, model.GloOther.Domain, model.GloOther.Domain, "已转交至下一操作人")
		}

		if t == 6 {
			ding = dingMsgTplHandler("已撤销", o)
			mail = fmt.Sprintf(TmplMail, "提交", o.WorkId, o.Username, model.GloOther.Domain, model.GloOther.Domain, "已撤销")
		}
	}

	if model.GloMessage.Mail {
		for _, i := range s.ToUser {
			if i.Email != "" {
				go SendMail(i.Email, s.Message, mail)
			}
		}
	}
	if model.GloMessage.Ding {
		if model.GloMessage.WebHook != "" {
			go SendDingMsg(s.Message, ding)
		}
	}
}
