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
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

const (
	WEBHOOK_TEST      = "测试消息已发送！请注意查收！"
	MAIL_TEST         = "测试邮件已发送！请注意查收！"
	ERR_LDAP_TEST     = "ldap连接失败,请检查配置/测试用户密码！"
	SUCCESS_LDAP_TEST = "ldap连接成功!"
)

type set struct {
	Ldap    model.Ldap    `json:"ldap"`
	Message model.Message `json:"message"`
	Other   model.Other   `json:"other"`
}

type delOrder struct {
	Date []string `json:"date"`
	Tp   bool     `json:"tp"`
}

func SuperFetchSetting(c yee.Context) (err error) {

	var k model.CoreGlobalConfiguration

	model.DB().Select("ldap,message,other").First(&k)

	return c.JSON(http.StatusOK, commom.SuccessPayload(k))
}

func SuperSaveSetting(c yee.Context) (err error) {

	u := new(set)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err)
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	other, _ := json.Marshal(u.Other)
	message, _ := json.Marshal(u.Message)
	ldap, _ := json.Marshal(u.Ldap)

	if !u.Other.Query {
		model.DB().Model(model.CoreQueryOrder{}).Where("`status` in (?)", []int{1, 2}).Updates(&model.CoreQueryOrder{Status: 3})
	}

	model.DB().Model(model.CoreGlobalConfiguration{}).Updates(&model.CoreGlobalConfiguration{Other: other, Message: message, Ldap: ldap})
	model.GloOther = u.Other
	model.GloLdap = u.Ldap
	model.GloMessage = u.Message
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.DATA_IS_EDIT))
}

func SuperTestSetting(c yee.Context) (err error) {

	el := c.QueryParam("test")
	u := new(set)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	switch el {
	case "mail":
		go lib.SendMail(u.Message.ToUser, u.Message, lib.TemoplateTestMail)
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(MAIL_TEST))
	case "ding":
		go lib.SendDingMsg(u.Message, lib.TmplTestDing)
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(WEBHOOK_TEST))
	case "ldap":
		ldap := model.ALdap{Ldap: u.Ldap}
		k, err := ldap.LdapConnect("", "", true)
		if err != nil {
			c.Logger().Error(err)
			return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(ERR_LDAP_TEST))
		}
		if k {
			return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(SUCCESS_LDAP_TEST))
		}
	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
}

func SuperDelOrder(c yee.Context) (err error) {
	u := new(delOrder)
	if err := c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	if u.Tp {
		go func() {
			if len(u.Date) == 2 {
				var order []model.CoreSqlOrder
				tx := model.DB().Begin()
				model.DB().Select("work_id").Where("`date` >= ? and `date` <= ? ", u.Date[0], u.Date[1]).Find(&order).Delete(&model.CoreQueryOrder{})
				for _, i := range order {
					tx.Where("work_id =?", i.WorkId).Delete(&model.CoreQueryRecord{})
				}
				tx.Commit()
			}
		}()
	} else {
		go func() {
			if len(u.Date) == 2 {
				var order []model.CoreSqlOrder
				model.DB().Select("work_id").Where("`date` >= ? and `date` <= ? ", u.Date[0], u.Date[1]).Find(&order).Delete(&model.CoreSqlOrder{})
				tx := model.DB().Begin()
				for _, i := range order {
					tx.Where("work_id =?", i.WorkId).Delete(&model.CoreSqlOrder{})
					tx.Where("work_id =?", i.WorkId).Delete(&model.CoreRollback{})
					tx.Where("work_id =?", i.WorkId).Delete(&model.CoreSqlRecord{})
				}
				tx.Commit()
			}
		}()
	}
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_DELETE))
}
