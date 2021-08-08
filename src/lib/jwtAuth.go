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

func JwtAuth(username string, role string) (t string, err error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)
	claims["name"] = username
	claims["role"] = role
	claims["exp"] = time.Now().Add(time.Hour * 8).Unix()
	t, err = token.SignedString([]byte(model.JWT))
	if err != nil {
		return "", errors.New("JWT Generate Failure")
	}
	return t, nil
}

func JwtParse(c yee.Context) (string, string) {
	user := c.Get("auth").(*jwt.Token)
	claims := user.Claims.(jwt.MapClaims)
	return claims["name"].(string), claims["role"].(string)
}
