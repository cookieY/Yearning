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
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/yee"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
)

func OidcState(c yee.Context) (err error) {

	oidcAuthUrl := fmt.Sprintf(
		"%s?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&state=367126378168",
		model.C.Oidc.AuthUrl,
		model.C.Oidc.ClientId,
		model.C.Oidc.RedirectUrL,
		model.C.Oidc.Scope)
	oidcEnable := model.C.Oidc.Enable
	if oidcEnable {
		return c.JSON(http.StatusOK, common.SuccessPayload(map[string]interface{}{
			"authUrl": oidcAuthUrl,
			"enabled": oidcEnable,
		}))
	} else {
		return c.JSON(http.StatusOK, common.SuccessPayload(map[string]interface{}{
			"enabled": false,
		}))
	}
}

func OidcLogin(c yee.Context) (err error) {

	if !model.C.Oidc.Enable {
		return c.HTML(400, "未开启 OIDC 登录")
	}

	code := c.FormValue("code")
	sessionState := c.FormValue(model.C.Oidc.SessionKey)

	if code == "" || sessionState == "" {
		authUri := fmt.Sprintf(
			"%s?response_type=code&client_id=%s&redirect_uri=%s&scope=%s",
			model.C.Oidc.AuthUrl,
			model.C.Oidc.ClientId,
			model.C.Oidc.RedirectUrL,
			model.C.Oidc.Scope)
		return c.Redirect(302, authUri)
	}
	account, err := getAccount(code, sessionState)

	token, tokenErr := lib.JwtAuth(lib.Token{
		Username: account.Username,
		RealName: account.RealName,
		IsRecord: account.IsRecorder == 1,
	})
	if tokenErr != nil {
		c.Logger().Error(tokenErr.Error())
		return
	}

	return c.Redirect(302, fmt.Sprintf(
		"/#/login?oidcLogin=1&token=%s&user=%s&real_name=%s&is_record=%s",
		token, account.Username, account.RealName, account.IsRecorder),
	)
}

func getAccount(code string, session_state string) (ac *model.CoreAccount, err error) {
	oidcToken, err := getOidcToken(code, session_state)
	if err != nil {
		return nil, err
	}
	userMap, err := getOidcUser(oidcToken)
	if err != nil {
		return nil, err
	}
	username := userMap[model.C.Oidc.UserNameKey].(string)
	realname := userMap[model.C.Oidc.RealNameKey].(string)
	email := userMap[model.C.Oidc.EmailKey].(string)

	var account = new(model.CoreAccount)
	if err := model.DB().Where("username = ?", username).First(&account).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		coreAccount := model.CoreAccount{
			Username:   username,
			RealName:   realname,
			Password:   lib.DjangoEncrypt(lib.GenWorkid(), string(lib.GetRandom())),
			Department: "",
			Email:      email,
		}
		model.DB().Create(&coreAccount)
		ix, _ := json.Marshal([]string{})
		model.DB().Create(&model.CoreGrained{Username: username, Group: ix})
	}
	model.DB().Where("username = ?", username).First(&account)

	return account, nil
}

func getOidcUser(token *OidcToken) (userMap map[string]interface{}, err error) {

	bearer := "Bearer " + token.AccessToken
	request, err := http.NewRequest("GET", model.C.Oidc.UserUrl, nil)
	request.Header.Add("Authorization", bearer)

	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(response.Body)

	userMap = make(map[string]interface{})
	err = json.NewDecoder(response.Body).Decode(&userMap)
	if err != nil {
		return nil, err
	}
	return userMap, nil
}

func getOidcToken(code string, session_state string) (oidc_token *OidcToken, err error) {
	resp, err := http.PostForm(model.C.Oidc.TokenUrl, url.Values{
		model.C.Oidc.SessionKey: {session_state},
		"code":                  {code},
		"client_id":             {model.C.Oidc.ClientId},
		"client_secret":         {model.C.Oidc.ClientSecret},
		"grant_type":            {"authorization_code"},
		"redirect_uri":          {model.C.Oidc.RedirectUrL},
	})
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	token := new(OidcToken)
	err = json.NewDecoder(resp.Body).Decode(token)
	if err != nil {
		return nil, err
	}
	return token, nil
}

type OidcToken struct {
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
}
