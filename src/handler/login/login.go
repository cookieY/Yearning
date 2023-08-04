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
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"errors"
	"github.com/cookieY/yee"
	"gorm.io/gorm"
	"net/http"
)

type loginForm struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func UserLdapLogin(c yee.Context) (err error) {
	u := new(loginForm)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	ldap := model.ALdap{Ldap: model.GloLdap}
	isOk, err := ldap.LdapConnect(u.Username, u.Password, false)
	if err != nil {
		return c.JSON(http.StatusOK, common.ERR_COMMON_MESSAGE(err))
	}
	if isOk {
		var account model.CoreAccount
		if err := model.DB().Where("username = ?", u.Username).First(&account).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			model.DB().Create(&model.CoreAccount{
				Username:   u.Username,
				RealName:   ldap.RealName,
				Password:   lib.DjangoEncrypt(lib.GenWorkid(), string(lib.GetRandom())),
				Department: ldap.Department,
				Email:      ldap.Email,
			})
			ix, _ := json.Marshal([]string{})
			model.DB().Create(&model.CoreGrained{Username: u.Username, Group: ix})
		}

		token, tokenErr := lib.JwtAuth(lib.Token{
			Username: u.Username,
			RealName: account.RealName,
			IsRecord: account.IsRecorder == 1,
		})
		if tokenErr != nil {
			c.Logger().Error(tokenErr.Error())
			return
		}
		dataStore := map[string]interface{}{
			"token":     token,
			"real_name": account.RealName,
			"user":      u.Username,
			"is_record": account.IsRecorder,
		}
		return c.JSON(http.StatusOK, common.SuccessPayload(dataStore))
	}
	return c.JSON(http.StatusOK, common.ERR_LOGIN)
}

func UserGeneralLogin(c yee.Context) (err error) {
	u := new(loginForm)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	var account model.CoreAccount
	if err := model.DB().Where("username = ?", u.Username).First(&account).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		if account.Username != u.Username {
			return c.JSON(http.StatusOK, common.ERR_LOGIN)
		}
		if e := lib.DjangoCheckPassword(&account, u.Password); e {
			token, tokenErr := lib.JwtAuth(lib.Token{
				Username: u.Username,
				RealName: account.RealName,
				IsRecord: account.IsRecorder == 1,
			})

			if tokenErr != nil {
				c.Logger().Error(tokenErr.Error())
				return err
			}
			dataStore := map[string]interface{}{
				"token":     token,
				"real_name": account.RealName,
				"user":      account.Username,
				"is_record": account.IsRecorder,
			}
			return c.JSON(http.StatusOK, common.SuccessPayload(dataStore))
		}

	}
	return c.JSON(http.StatusOK, common.ERR_LOGIN)

}

func UserRegister(c yee.Context) (err error) {

	if model.GloOther.Register {
		u := new(model.CoreAccount)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
		}
		var unique model.CoreAccount
		ix, _ := json.Marshal([]string{})
		if model.DB().Where("username = ?", u.Username).Select("username").First(&unique).Error == gorm.ErrRecordNotFound {
			model.DB().Create(&model.CoreAccount{
				Username:   u.Username,
				RealName:   u.RealName,
				Password:   lib.DjangoEncrypt(u.Password, string(lib.GetRandom())),
				Department: u.Department,
				Email:      u.Email,
			})
			model.DB().Create(&model.CoreGrained{Username: u.Username, Group: ix})
			return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.INFO_REGISTRATION_SUCCESS)))
		}
		return c.JSON(http.StatusOK, common.ERR_COMMON_TEXT_MESSAGE(i18n.DefaultLang.Load(i18n.ER_USER_ALREADY_EXISTS)))
	}
	return c.JSON(http.StatusOK, common.ERR_REGISTER)

}
