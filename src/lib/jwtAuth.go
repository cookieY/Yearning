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
	"errors"
	"github.com/cookieY/yee"
	"github.com/golang-jwt/jwt"
	"time"
)

type Token struct {
	Username string
	RealName string
	IsRecord bool
}

func JwtAuth(h Token) (t string, err error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)
	claims["name"] = h.Username
	claims["real_name"] = h.RealName
	claims["is_record"] = h.IsRecord
	claims["exp"] = time.Now().Add(time.Hour * 8).Unix()
	t, err = token.SignedString([]byte(model.JWT))
	if err != nil {
		return "", errors.New("JWT Generate Failure")
	}
	return t, nil
}

func (h *Token) JwtParse(c yee.Context) *Token {
	user := c.Get("auth").(*jwt.Token)
	claims := user.Claims.(jwt.MapClaims)
	h.Username = claims["name"].(string)
	h.RealName = claims["real_name"].(string)
	h.IsRecord = claims["is_record"].(bool)
	return h
}

func WSTokenIsValid(token string) (bool, error) {
	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(model.JWT), nil
	})
	return t.Valid, err
}

func WsTokenParse(token string) (*jwt.Token, error) {
	return jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(model.JWT), nil
	})
}
