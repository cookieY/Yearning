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

package service

import (
	"Yearning-go/src/model"
	_ "Yearning-go/src/model"
	"Yearning-go/src/parser"
	"Yearning-go/src/router"
	"encoding/json"
	"github.com/cookieY/yee"
	"github.com/cookieY/yee/middleware"
	"github.com/gobuffalo/packr/v2"
	"net/http"
)

func StartYearning(addr string, host string) {
	box := packr.New("gemini", "./dist")
	model.DB().First(&model.GloPer)
	model.Host = host
	_ = json.Unmarshal(model.GloPer.Message, &model.GloMessage)
	_ = json.Unmarshal(model.GloPer.Ldap, &model.GloLdap)
	_ = json.Unmarshal(model.GloPer.Other, &model.GloOther)
	_ = json.Unmarshal(model.GloPer.AuditRole, &parser.FetchAuditRole)
	e := yee.New()
	e.Packr("/front", http.FileSystem(box))
	e.Use(middleware.Cors())
	e.Use(middleware.Logger())
	e.Use(middleware.Secure())
	e.Use(middleware.Recovery())
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 5,
	}))
	router.AddRouter(e, box)
	e.Run(addr)
}
