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

package model

import (
	"Yearning-go/src/engine"
	"github.com/cookieY/yee/logger"
	"time"
)

var mappingLevel = map[string]uint8{
	"critical": 0,
	"error":    1,
	"warning":  2,
	"info":     3,
	"debug":    4,
}

type mysql struct {
	Host     string
	User     string
	Password string
	Db       string
	Port     string
}

type general struct {
	SecretKey string
	Host      string
	Hours     time.Duration
	RpcAddr   string
	LogLevel  string
	Lang      string
}

type DbInfo struct {
	Host     string
	User     string
	Password string
	Port     string
	Db       string
}

type oidc struct {
	Enable       bool
	ClientId     string
	ClientSecret string
	Scope        string
	AuthUrl      string
	TokenUrl     string
	UserUrl      string
	RedirectUrL  string
	SessionKey   string
	UserNameKey  string
	RealNameKey  string
	EmailKey     string
}

type Config struct {
	General general
	Mysql   mysql
	Oidc    oidc
}

var C Config

var DefaultLogger logger.Logger

var JWT = ""

var GloPer CoreGlobalConfiguration

var GloLdap Ldap

var GloOther Other

var GloMessage Message

var GloRole engine.AuditRole

func TransferLogLevel() uint8 {
	v, ok := mappingLevel[C.General.LogLevel]
	if !ok {
		return 3
	}
	return v
}
