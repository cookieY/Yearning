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
	"gopkg.in/gomail.v2"
	"log"
)

type UserInfo struct {
	ToUser  string
	User    string
	Pawd    string
	Smtp    string
	PubName string
}

var TemoplateTestMail = `
<html>
<body>
	<div style="text-align:center;">
		<h1>Yearning 2.0</h1>
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

var TmplTestDing = `# Yearning 测试！`

var TmplReferDing = `# Yearning工单提交通知 #  \n \n  **工单编号:**  %s \n \n **数据源:** %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **下一步操作人:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#1abefa\">已提交</font> \n \n `

var TmplRejectDing = `# Yearning工单驳回通知 #  \n \n  **工单编号:**  %s \n \n **数据源:** %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **下一步操作人:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#df117e\">驳回</font>  \n \n **驳回说明:**  %s `
var TmplSuccessDing = `# Yearning工单执行通知 #  \n \n  **工单编号:**  %s \n \n **数据源:** %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **执行人:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#3fd2bd\">执行成功</font>`
var TmplFailedDing = `# Yearning工单执行通知 #  \n \n  **工单编号:**  %s \n \n **数据源:** %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **执行人:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#ea2426\">执行失败</font>`
var TmplPerformDing = `# Yearning工单转交通知 #  \n \n  **工单编号:**  %s \n \n **数据源:** %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **下一步操作人:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#de4943\">已转交至下一操作人</font>`
var TmplBackDing = `# Yearning工单执行通知 #  \n \n  **工单编号:**  %s \n \n **数据源:** %s  \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **下一步操作人:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#ea2426\">已撤回</font>`

var TmplQueryRefer = `# Yearning查询申请通知 #  \n \n  **工单编号:**  %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **审核人员:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **工单说明:**  %s \n \n **状态:** <font color=\"#1abefa\">已提交</font>`
var TmplSuccessQuery = `# Yearning查询申请通知 #  \n \n  **工单编号:**  %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **审核人员:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **状态:** <font color=\"#3fd2bd\">同意</font>`
var TmplRejectQuery = `# Yearning查询申请通知 #  \n \n  **工单编号:**  %s \n \n **提交人员:**  <font color=\"#78beea\">%s</font> \n \n **审核人员:** <font color=\"#fe8696\">%s</font> \n \n **平台地址:** %s \n \n **状态:** <font color=\"#df117e\">已驳回</font>`

func SendMail(mail model.Message, tmpl string) {
	m := gomail.NewMessage()
	m.SetHeader("From", mail.User)
	m.SetHeader("To", mail.ToUser)
	m.SetHeader("Subject", "Yearning消息推送!")
	m.SetBody("text/html", tmpl)
	d := gomail.NewDialer(mail.Host, mail.Port, mail.User, mail.Password)
	if mail.Ssl {
		d.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	}
	// Send the email to Bob, Cora and Dan.
	if err := d.DialAndSend(m); err != nil {
		log.Println(err.Error())
		return
	}
}

func MessagePush(workid string, t uint, reject string) {
	var user model.CoreAccount
	var o model.CoreSqlOrder
	var ding, mail string
	model.DB().Select("work_id,username,text,assigned,executor,source").Where("work_id =?", workid).First(&o)
	model.DB().Select("email").Where("username =?", o.Username).First(&user)
	s := model.GloMessage
	s.ToUser = user.Email

	if model.GloOther.Query && t > 6 {
		var op model.CoreQueryOrder
		model.DB().Select("work_id,username,text,assigned").Where("work_id =?", workid).First(&op)
		model.DB().Select("email").Where("username =?", op.Username).First(&user)
		s.ToUser = user.Email
		if t == 7 {
			model.DB().Select("email").Where("username =?", op.Assigned).First(&user)
			s.ToUser = user.Email
			ding = fmt.Sprintf(TmplQueryRefer, op.WorkId, op.Username, op.Assigned, model.Host, op.Text)
			mail = fmt.Sprintf(TmplMail, "查询申请", op.WorkId, op.Username, model.Host, model.Host, "已提交")
		}
		if t == 8 {
			ding = fmt.Sprintf(TmplSuccessQuery, op.WorkId, op.Username, op.Assigned, model.Host)
			mail = fmt.Sprintf(TmplMail, "查询申请", op.WorkId, op.Username, model.Host, model.Host, "已同意")
		}
		if t == 9 {
			ding = fmt.Sprintf(TmplRejectQuery, op.WorkId, op.Username, op.Assigned, model.Host)
			mail = fmt.Sprintf(TmplMail, "查询申请", op.WorkId, op.Username, model.Host, model.Host, "已驳回")
		}
	} else {
		switch t {
		case 0:
			ding = fmt.Sprintf(TmplRejectDing, o.WorkId, o.Source, o.Username, o.Assigned, model.Host, o.Text, reject)
			mail = fmt.Sprintf(TmplRejectMail, o.WorkId, o.Username, model.Host, model.Host, reject)
		case 1:
			ding = fmt.Sprintf(TmplSuccessDing, o.WorkId, o.Source, o.Username, o.Assigned, model.Host, o.Text)
			mail = fmt.Sprintf(TmplMail, "执行", o.WorkId, o.Username, model.Host, model.Host, "执行成功")
		case 2:
			model.DB().Select("email").Where("username =?", o.Assigned).First(&user)
			s.ToUser = user.Email
			ding = fmt.Sprintf(TmplReferDing, o.WorkId, o.Source, o.Username, o.Assigned, model.Host, o.Text)
			mail = fmt.Sprintf(TmplMail, "提交", o.WorkId, o.Username, model.Host, model.Host, "已提交")
		case 4:
			ding = fmt.Sprintf(TmplFailedDing, o.WorkId, o.Source, o.Username, o.Assigned, model.Host, o.Text)
			mail = fmt.Sprintf(TmplMail, "执行", o.WorkId, o.Username, model.Host, model.Host, "执行失败")
		case 5:
			model.DB().Select("email").Where("username =?", o.Assigned).First(&user)
			s.ToUser = user.Email
			ding = fmt.Sprintf(TmplPerformDing, o.WorkId, o.Source, o.Username, o.Assigned, model.Host, o.Text)
			mail = fmt.Sprintf(Tmpl2Mail, "转交", o.WorkId, o.Username, o.Assigned, model.Host, model.Host, "已转交至下一操作人")
		case 6:
			ding = fmt.Sprintf(TmplBackDing, o.WorkId, o.Source, o.Username, o.Assigned, model.Host, o.Text)
			mail = fmt.Sprintf(TmplMail, "提交", o.WorkId, o.Username, model.Host, model.Host, "已撤销")
		}
	}

	if model.GloMessage.Mail {
		if user.Email != "" {
			go SendMail(s, mail)
		}
	}
	if model.GloMessage.Ding {
		if model.GloMessage.WebHook != "" {
			go SendDingMsg(s, ding)
		}
	}
}
