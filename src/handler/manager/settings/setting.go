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

package settings

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

const (
	WEBHOOK_TEST      = "测试消息已发送！请注意查收！"
	MAIL_TEST         = "测试邮件已发送！请注意查收！"
	ERR_LDAP_TEST     = "ldap连接失败!"
	SUCCESS_LDAP_TEST = "ldap连接成功!"
)

type set struct {
	Ldap    model.Ldap
	Message model.Message
	Other   model.Other
	Mail    model.Message
}

type ber struct {
	Date string `json:"date"`
	Tp   bool   `json:"tp"`
}

func SuperFetchSetting(c yee.Context) (err error) {

	var k model.CoreGlobalConfiguration

	model.DB().Select("ldap,message,other").First(&k)

	return c.JSON(http.StatusOK, commom.SuccessPayload(k))
}

func SuperSaveSetting(c yee.Context) (err error) {

	u := new(set)

	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	other, _ := json.Marshal(u.Other)
	message, _ := json.Marshal(u.Message)
	ldap, _ := json.Marshal(u.Ldap)
	diffIDC(u.Other.IDC)
	model.DB().Model(model.CoreGlobalConfiguration{}).Updates(&model.CoreGlobalConfiguration{Other: other, Message: message, Ldap: ldap})
	model.GloOther = u.Other
	model.GloLdap = u.Ldap
	model.GloMessage = u.Message
	lib.OverrideConfig(&pb.LibraAuditOrder{})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_EDIT))
}

func diffIDC(src []string) {
	var idc model.CoreGlobalConfiguration
	var env model.Other
	model.DB().Find(&idc)
	_ = json.Unmarshal(idc.Other, &env)
	p := lib.NonIntersect(src, env.IDC)
	for _, i := range p {
		model.DB().Model(model.CoreWorkflowTpl{}).Where("source =?", i).Delete(&model.CoreWorkflowTpl{})
	}
}

func SuperTestSetting(c yee.Context) (err error) {

	el := c.QueryParam("test")
	u := new(set)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	if el == "mail" {
		go lib.SendMail(u.Mail, lib.TemoplateTestMail)
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(MAIL_TEST))
	}

	if el == "ding" {
		go lib.SendDingMsg(u.Mail, lib.TmplTestDing)
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(WEBHOOK_TEST))
	}

	if el == "ldap" {
		if k, _ := lib.LdapContent(&u.Ldap, "", "", true); k {
			return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(SUCCESS_LDAP_TEST))
		}
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(ERR_LDAP_TEST))

	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
}

func SuperDelOrder(c yee.Context) (err error) {
	u := new(ber)
	if err := c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	if u.Tp {
		go func() {
			var order []model.CoreQueryOrder
			model.DB().Where("`date` < ?", u.Date).Find(&order)

			tx := model.DB().Begin()
			for _, i := range order {
				model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryOrder{})
				model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryRecord{})
			}
			tx.Commit()
		}()
	} else {
		go func() {
			var order []model.CoreSqlOrder
			model.DB().Where("`date` < ?", u.Date).Find(&order)
			tx := model.DB().Begin()
			for _, i := range order {
				tx.Where("work_id =?", i.WorkId).Delete(&model.CoreSqlOrder{})
				tx.Where("work_id =?", i.WorkId).Delete(&model.CoreRollback{})
				tx.Where("work_id =?", i.WorkId).Delete(&model.CoreSqlRecord{})
			}
			tx.Commit()
		}()
	}
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_DELETE))
}
