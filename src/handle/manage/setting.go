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

package manage

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	ser "Yearning-go/src/parser"
	pb "Yearning-go/src/proto"
	"encoding/json"
	"github.com/cookieY/yee"
	"net/http"
)

type set struct {
	Ldap    model.Ldap
	Message model.Message
	Other   model.Other
	Juno    ser.AuditRole
}

type ber struct {
	Date string `json:"date"`
}

type mt struct {
	Mail model.Message
}

type lt struct {
	Ldap model.Ldap
}

func SuperFetchSetting(c yee.Context) (err error) {

	var k model.CoreGlobalConfiguration

	model.DB().First(&k)

	return c.JSON(http.StatusOK, k)
}

func SuperSaveSetting(c yee.Context) (err error) {

	u := new(set)

	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}

	other, _ := json.Marshal(u.Other)
	message, _ := json.Marshal(u.Message)
	ldap, _ := json.Marshal(u.Ldap)
	model.DB().Model(model.CoreGlobalConfiguration{}).Where("authorization =?", "global").Updates(&model.CoreGlobalConfiguration{Other: other, Message: message, Ldap: ldap})
	model.GloOther = u.Other
	model.GloLdap = u.Ldap
	model.GloMessage = u.Message

	return c.JSON(http.StatusOK, "配置信息已保存！")
}

func SuperTestSetting(c yee.Context) (err error) {

	el := c.Params("el")

	if el == "mail" {
		u := new(mt)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}
		go lib.SendMail(u.Mail, lib.TemoplateTestMail)
		return c.JSON(http.StatusOK, "测试邮件已发送！请注意查收！")
	}

	if el == "ding" {
		u := new(mt)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}
		go lib.SendDingMsg(u.Mail, lib.TmplTestDing)
		return c.JSON(http.StatusOK, "测试消息已发送！请注意查收！")
	}

	if el == "ldap" {
		ld := new(lt)
		if err = c.Bind(ld); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}

		if k := lib.LdapConnenct(c, &ld.Ldap, "", "", true); k {
			return c.JSON(http.StatusOK, "ldap连接成功!")
		} else {
			return c.JSON(http.StatusOK, "ldap连接失败!")
		}

	}

	return c.JSON(http.StatusInternalServerError, "未知传参！")
}

func SuperSaveRoles(c yee.Context) (err error) {

	u := new(set)

	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	ser.FetchAuditRole = u.Juno
	audit, _ := json.Marshal(u.Juno)
	model.DB().Model(model.CoreGlobalConfiguration{}).Where("authorization =?", "global").Updates(&model.CoreGlobalConfiguration{AuditRole: audit})
	lib.OverrideConfig(&pb.LibraAuditOrder{})
	return c.JSON(http.StatusOK, "配置信息已保存！")
}

func DelQueryOrder(c yee.Context) (err error) {
	u := new(ber)
	if err := c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, err.Error())
	}
	var order []model.CoreQueryOrder
	model.DB().Where("`date` < ?", u.Date).Find(&order)

	tx := model.DB().Begin()
	for _, i := range order {
		model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryOrder{})
		model.DB().Where("work_id =?", i.WorkId).Delete(&model.CoreQueryRecord{})
	}
	tx.Commit()

	return c.JSON(http.StatusOK, "查询工单删除!")
}

// UndoAuditOrder delete Order
func UndoAuditOrder(c yee.Context) (err error) {
	u := new(ber)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
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
	return c.JSON(http.StatusOK, "工单已删除！")
}
