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

package user

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/cookieY/yee"
)

type userInfo struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	New        string `json:"new"`
	Mail       string `json:"mail"`
	RealName   string `json:"real_name"`
	Rule       string `json:"rule"`
	Department string `json:"department"`
	Valve      bool   `json:"valve"`
	Tp         string `json:"tp"`
	Group      string `json:"group"`
}

type register struct {
	UserInfo userInfo `json:"user_info"`
}

func UserLdapLogin(c yee.Context) (err error) {
	var account model.CoreAccount
	u := new(userInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	if lib.LdapConnenct(c, &model.GloLdap, u.Username, u.Password, false) {
		if model.DB().Where("username = ?", u.Username).First(&account).RecordNotFound() {
			model.DB().Create(&model.CoreAccount{
				Username:   u.Username,
				RealName:   "请重置你的真实姓名",
				Password:   lib.DjangoEncrypt(lib.GenWorkid(), string(lib.GetRandom())),
				Rule:       "guest",
				Department: "all",
				Email:      "",
			})
			ix, _ := json.Marshal([]string{})
			model.DB().Create(&model.CoreGrained{Username: u.Username, Group: ix})
		}
		token, tokenErr := lib.JwtAuth(u.Username, account.Rule)
		if tokenErr != nil {
			c.Logger().Error(tokenErr.Error())
			return
		}
		dataStore := map[string]string{
			"token":       token,
			"permissions": account.Rule,
			"real_name":   account.RealName,
		}
		return c.JSON(http.StatusOK, dataStore)
	}
	return c.JSON(http.StatusUnauthorized, "")
}

func UserGeneralLogin(c yee.Context) (err error) {
	u := new(userInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var account model.CoreAccount
	if !model.DB().Where("username = ?", u.Username).First(&account).RecordNotFound() {
		if e := lib.DjangoCheckPassword(&account, u.Password); e {
			token, tokenErr := lib.JwtAuth(u.Username, account.Rule)
			if tokenErr != nil {
				c.Logger().Error(tokenErr.Error())
				return
			}
			dataStore := map[string]string{
				"token":       token,
				"permissions": account.Rule,
				"real_name":   account.RealName,
			}
			return c.JSON(http.StatusOK, dataStore)
		}

	}
	return c.JSON(http.StatusUnauthorized, "")

}

func UserReqSwitch(c yee.Context) (err error) {
	if model.GloOther.Register {
		return c.JSON(http.StatusOK, map[string]interface{}{"reg": 1, "valid": true})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"reg": 0, "valid": true})
}

func UserRegister(c yee.Context) (err error) {

	if model.GloOther.Register {
		u := new(register)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, "")
		}
		var unique model.CoreAccount
		ix, _ := json.Marshal([]string{})
		model.DB().Where("username = ?", u.UserInfo.Username).Select("username").First(&unique)
		if unique.Username != "" {
			return c.JSON(http.StatusOK, "用户已存在请重新注册！")
		}
		model.DB().Create(&model.CoreAccount{
			Username:   u.UserInfo.Username,
			RealName:   u.UserInfo.RealName,
			Password:   lib.DjangoEncrypt(u.UserInfo.Password, string(lib.GetRandom())),
			Rule:       "guest",
			Department: u.UserInfo.Department,
			Email:      u.UserInfo.Mail,
		})
		model.DB().Create(&model.CoreGrained{Username: u.UserInfo.Username, Group: ix})
		return c.JSON(http.StatusOK, "注册成功！")
	}
	return c.JSON(http.StatusForbidden, "没有开启注册通道！")

}

func GeneralUserEdit(c yee.Context) (err error) {
	param := c.Params("tp")

	u := new(userInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	user, _ := lib.JwtParse(c)
	switch param {
	case "password":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user).Update("password", lib.DjangoEncrypt(u.New, string(lib.GetRandom())))
		return c.JSON(http.StatusOK, "密码修改成功！")
	case "mail":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", user).Updates(model.CoreAccount{Email: u.Mail, RealName: u.RealName})
		return c.JSON(http.StatusOK, "邮箱/真实姓名修改成功！刷新后显示最新信息!")
	default:
		return c.JSON(http.StatusOK, "Forbidden")
	}
}

func SuperUserRegister(c yee.Context) (err error) {

	u := new(register)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	var unique model.CoreAccount
	model.DB().Where("username = ?", u.UserInfo.Username).Select("username").First(&unique)
	if unique.Username != "" {
		return c.JSON(http.StatusOK, "用户已存在请重新注册！")
	}
	model.DB().Create(&model.CoreAccount{
		Username:   u.UserInfo.Username,
		RealName:   u.UserInfo.RealName,
		Password:   lib.DjangoEncrypt(u.UserInfo.Password, string(lib.GetRandom())),
		Rule:       u.UserInfo.Group,
		Department: u.UserInfo.Department,
		Email:      u.UserInfo.Mail,
	})
	ix, _ := json.Marshal([]string{})
	model.DB().Create(&model.CoreGrained{Username: u.UserInfo.Username,Group: ix})
	return c.JSON(http.StatusOK, "注册成功！")
}

func SuperUserEdit(c yee.Context) (err error) {
	u := new(userInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	switch u.Tp {
	case "info":
		tx := model.DB().Begin()
		tx.Model(model.CoreAccount{}).Where("username = ?", u.Username).Updates(model.CoreAccount{Email: u.Mail, RealName: u.RealName, Department: u.Department, Rule: u.Rule})
		tx.Model(model.CoreSqlOrder{}).Where("username =?", u.Username).Update(model.CoreSqlOrder{RealName: u.RealName})
		tx.Model(model.CoreQueryOrder{}).Where("username =?", u.Username).Update(model.CoreQueryOrder{Realname: u.RealName})
		tx.Commit()
		return c.JSON(http.StatusOK, "邮箱/真实姓名修改成功！")
	case "password":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", u.Username).Update("password", lib.DjangoEncrypt(u.New, string(lib.GetRandom())))
		return c.JSON(http.StatusOK, "密码修改成功！")
	default:
		return c.JSON(http.StatusOK, "Forbidden")
	}

}

func SuperFetchUser(c yee.Context) (err error) {
	var f userInfo
	var u []model.CoreAccount
	var pg int
	con := c.QueryParam("con")
	if err := json.Unmarshal([]byte(con), &f); err != nil {
		c.Logger().Error(err.Error())
	}
	start, end := lib.Paging(c.QueryParam("page"), 10)

	if f.Valve {
		model.DB().Model(model.CoreAccount{}).Where("username LIKE ? and department LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%", "%"+fmt.Sprintf("%s", f.Department)+"%").Count(&pg).Offset(start).Limit(end).Find(&u)
	} else {
		model.DB().Model(model.CoreAccount{}).Count(&pg).Offset(start).Limit(end).Find(&u)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"page": pg, "data": u, "multi": model.GloOther.Multi})
}

func SuperDeleteUser(c yee.Context) (err error) {
	user := c.QueryParam("user")

	if user == "admin" {
		return c.JSON(http.StatusOK, "admin用户无法被删除!")
	}

	model.DB().Where("username =?",user).Delete(&model.CoreAccount{})

	return c.JSON(http.StatusOK, fmt.Sprintf("用户: %s 已删除", user))
}

func FetchUserPermissions(c yee.Context) (err error) {
	u := new(userInfo)
	if err = c.Bind(u); err != nil {
		return err
	}
	var p []model.CoreRoleGroup
	var userP model.CoreGrained
	model.DB().Find(&p)
	model.DB().Where("username=?", u.Username).First(&userP)
	return c.JSON(http.StatusOK, map[string]interface{}{"group": userP.Group, "list": p})
}

func FetchUserDepend(c yee.Context) (err error) {
	user := c.QueryParam("user")
	return c.JSON(http.StatusOK, DelUserDepend(user))
}

func SuperUserApi() yee.RestfulAPI {
	return yee.RestfulAPI{
		Put:    SuperUserEdit,
		Post:   SuperUserRegister,
		Delete: SuperDeleteUser,
		Get:    SuperFetchUser,
	}
}
