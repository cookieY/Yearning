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
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	_ "Yearning-go/src/model"
	"Yearning-go/src/router"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"github.com/cookieY/yee/logger"
	"github.com/cookieY/yee/middleware"
	"github.com/robfig/cron/v3"
	"net/http"
	"time"
)

//go:embed dist/*
var f embed.FS

//go:embed dist/index.html
var html string

func cronTabMaskQuery() {
	crontab := cron.New()
	if _, err := crontab.AddFunc("* * * * *", func() {
		var queryOrder []model.CoreQueryOrder
		model.DB().Model(model.CoreQueryOrder{}).Where("`status` =?", 2).Find(&queryOrder)
		for _, i := range queryOrder {
			if lib.TimeDifference(i.ApprovalTime) {
				model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", i.WorkId).Updates(&model.CoreQueryOrder{Status: 3})
			}
		}
	}); err != nil {
		logger.DefaultLogger.Error(err)
	}
	crontab.Start()
}

func cronTabTotalTickets() {
	crontab := cron.New()
	if _, err := crontab.AddFunc("15 2 * * *", func() {
		var totalOrder int64
		var totalQuery int64
		model.DB().Model(model.CoreSqlOrder{}).Where("DATE(date) = CURDATE() - INTERVAL 1 DAY").Count(&totalOrder)
		model.DB().Model(model.CoreQueryOrder{}).Where("DATE(date) = CURDATE() - INTERVAL 1 DAY").Count(&totalQuery)
		model.DB().Model(model.CoreTotalTickets{}).Create(&model.CoreTotalTickets{TotalOrder: totalOrder, TotalQuery: totalQuery, Date: time.Now().Format("2006-01-02")})
	}); err != nil {
		logger.DefaultLogger.Error(err)
	}
	crontab.Start()
}

func StartYearning(port string, host string) {
	go cronTabMaskQuery()
	go cronTabTotalTickets()
	model.DB().First(&model.GloPer)
	model.Host = host
	_ = json.Unmarshal(model.GloPer.Message, &model.GloMessage)
	_ = json.Unmarshal(model.GloPer.Ldap, &model.GloLdap)
	_ = json.Unmarshal(model.GloPer.Other, &model.GloOther)
	_ = json.Unmarshal(model.GloPer.AuditRole, &model.GloRole)
	e := yee.New()
	e.Pack("/front", f, "dist")
	e.Use(middleware.Cors())
	e.Use(middleware.Logger())
	e.Use(middleware.Secure())
	e.Use(middleware.Recovery())
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 9,
	}))
	e.SetLogLevel(model.TransferLogLevel())
	e.GET("/", func(c yee.Context) error {
		return c.HTML(http.StatusOK, html)
	})
	router.AddRouter(e)

	e.Run(fmt.Sprintf("%s", port))
}
