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
	"fmt"
	"github.com/cookieY/yee"
	"github.com/golang-jwt/jwt"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func OidcState(c yee.Context) (err error) {

	oidcAuthUrl, _ := oidcAuthUrl(c)
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

func oidcAuthUrl(c yee.Context) (oidcAuthUrl string, state string) {
	state = generateState()
	return fmt.Sprintf(
		"%s?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&state=%s",
		model.C.Oidc.AuthUrl,
		model.C.Oidc.ClientId,
		model.C.Oidc.RedirectUrL,
		model.C.Oidc.Scope,
		state,
	), state
}

// 生成 state，state 格式为 hs265签名.当前分钟数
func generateState() string {
	curMinutes := strconv.FormatInt(time.Now().Unix()/60, 10)
	sign, err := jwt.GetSigningMethod("HS256").Sign(curMinutes, []byte(model.JWT))
	if err != nil {
		return ""
	}
	return sign + "." + curMinutes
}

// 由于没有session 机制，所以使用如下方法校验 state
// 2分钟之内的 state 有效
func validState(state string) error {
	split := strings.Split(state, ".")
	sign := split[0]
	curMinutes := time.Now().Unix() / 60
	for i := 0; i < 3; i++ {
		err := jwt.GetSigningMethod("HS256").Verify(strconv.FormatInt(curMinutes-int64(i), 10), sign, []byte(model.JWT))
		if err == nil {
			return nil
		}
	}
	return errors.New("state error")
}

func OidcLogin(c yee.Context) (err error) {

	if !model.C.Oidc.Enable {
		return c.HTML(400, i18n.DefaultLang.Load(i18n.INFO_OIDC_LOGIN_DISABLED))
	}

	code := c.FormValue("code")
	state := c.FormValue("state")
	sessionState := c.FormValue(model.C.Oidc.SessionKey)

	if code == "" || state == "" {
		oidcAuthUrl, _ := oidcAuthUrl(c)
		return c.Redirect(302, oidcAuthUrl)
	}
	err = validState(state)
	if err != nil {
		oidcAuthUrl, _ := oidcAuthUrl(c)
		return c.Redirect(302, oidcAuthUrl)
	}

	account, err := getAccount(code, sessionState)
	if err != nil {
		oidcAuthUrl, _ := oidcAuthUrl(c)
		return c.Redirect(302, oidcAuthUrl)
	}

	token, tokenErr := lib.JwtAuth(lib.Token{
		Username: account.Username,
		RealName: account.RealName,
		IsRecord: account.IsRecorder == 1,
	})
	if tokenErr != nil {
		c.Logger().Error(tokenErr.Error())
		oidcAuthUrl, _ := oidcAuthUrl(c)
		return c.Redirect(302, oidcAuthUrl)
	}

	return c.Redirect(302, fmt.Sprintf(
		"/#/login?oidcLogin=1&token=%s&user=%s&real_name=%s&is_record=%d",
		token, account.Username, account.RealName, account.IsRecorder),
	)
}

func getAccount(code string, sessionState string) (ac *model.CoreAccount, err error) {
	oidcToken, err := getOidcToken(code, sessionState)
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

func getOidcToken(code string, sessionState string) (oidc_token *OidcToken, err error) {
	resp, err := http.PostForm(model.C.Oidc.TokenUrl, url.Values{
		model.C.Oidc.SessionKey: {sessionState},
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
