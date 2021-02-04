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

package login

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cookieY/yee"
)

type loginForm struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func UserLdapLogin(c yee.Context) (err error) {
	u := new(loginForm)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	isOk, err := lib.LdapContent(&model.GloLdap, u.Username, u.Password, false)
	if err != nil {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}
	if isOk {
		var account model.CoreAccount
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
		return c.JSON(http.StatusOK, commom.SuccessPayload(dataStore))
	}
	return c.JSON(http.StatusOK, commom.ERR_LOGIN)
}

func UserGeneralLogin(c yee.Context) (err error) {
	u := new(loginForm)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var account model.CoreAccount
	if !model.DB().Where("username = ?", u.Username).First(&account).RecordNotFound() {
		if account.Username != u.Username {
			return c.JSON(http.StatusOK, commom.ERR_LOGIN)
		}
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
			return c.JSON(http.StatusOK, commom.SuccessPayload(dataStore))
		}

	}
	return c.JSON(http.StatusOK, commom.ERR_LOGIN)

}

func UserRegister(c yee.Context) (err error) {

	if model.GloOther.Register {
		u := new(model.CoreAccount)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
		}
		var unique model.CoreAccount
		ix, _ := json.Marshal([]string{})
		model.DB().Where("username = ?", u.Username).Select("username").First(&unique)
		if unique.Username != "" {
			return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(errors.New("用户已存在请重新注册！")))
		}
		model.DB().Create(&model.CoreAccount{
			Username:   u.Username,
			RealName:   u.RealName,
			Password:   lib.DjangoEncrypt(u.Password, string(lib.GetRandom())),
			Rule:       "guest",
			Department: u.Department,
			Email:      u.Email,
		})
		model.DB().Create(&model.CoreGrained{Username: u.Username, Group: ix})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage("注册成功！"))
	}
	return c.JSON(http.StatusOK, commom.ERR_REGISTER)

}
