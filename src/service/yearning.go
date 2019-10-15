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
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func StartYearning(port string, host string) {
	model.DB().First(&model.GloPer)
	model.Host = host
	json.Unmarshal(model.GloPer.Message, &model.GloMessage)
	json.Unmarshal(model.GloPer.Ldap, &model.GloLdap)
	json.Unmarshal(model.GloPer.Other, &model.GloOther)
	json.Unmarshal(model.GloPer.AuditRole, &parser.FetchAuditRole)
	e := echo.New()
	e.Static("/", "dist")
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
	}))
	e.Use(middleware.Secure())
	e.Use(middleware.Recover())
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 5,
	}))
	router.AddRouter(e)
	e.Logger.SetLevel(1)
	e.Logger.Fatal(e.Start(fmt.Sprintf(":%s", port)))
}
