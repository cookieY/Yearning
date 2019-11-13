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

package router

import (
	"Yearning-go/src/handle"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"net/http"
)

func SuperManageDB(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if lib.SuperAuth(c, "db") {
			return next(c)
		}
		return c.JSON(http.StatusForbidden, "非法越权操作！")
	}
}

func SuperManageUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if lib.SuperAuth(c, "user") {
			return next(c)
		}
		return c.JSON(http.StatusForbidden, "非法越权操作！")
	}
}

func SuperManageGroup(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user, _ := lib.JwtParse(c)
		if user == "admin" {
			return next(c)
		}
		return c.JSON(http.StatusForbidden, "非法越权操作！")
	}
}

func AuditGroup(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		_, rule := lib.JwtParse(c)
		if rule == "admin" || rule == "perform" {
			return next(c)
		}
		return c.JSON(http.StatusForbidden, "非法越权操作！")
	}
}

func AddRouter(e *echo.Echo) {
	e.POST("/login", handle.UserGeneralLogin)
	e.POST("/register", handle.UserRegister)
	e.GET("/fetch", handle.UserReqSwitch)
	e.POST("/ldap", handle.UserLdapLogin)

	r := e.Group("/api/v2", middleware.JWT([]byte(model.JWT)))
	r.POST("/dash/initMenu", handle.DashInit)
	r.GET("/dash/pie", handle.DashPie)
	r.GET("/dash/axis", handle.DashAxis)
	r.GET("/dash/count", handle.DashCount)
	r.PUT("/dash/userinfo", handle.DashUserInfo)
	r.PUT("/dash/stmt", handle.DashStmt)
	r.POST("/dash/refer", handle.ReferGroupOrder)

	r.POST("/user/password_reset", handle.ChangePassword)
	r.POST("/user/mail_reset", handle.ChangeMail)
	r.PUT("/user/order", handle.GeneralFetchMyOrder)
	r.GET("/fetch/sql", handle.GeneralFetchSQLInfo)
	r.GET("/fetch/idc", handle.GeneralIDC)
	r.GET("/fetch/source/:idc/:xxx", handle.GeneralSource)
	r.GET("/fetch/base/:source", handle.GeneralBase)
	r.PUT("/fetch/table", handle.GeneralTable)
	r.PUT("/fetch/tableinfo", handle.GeneralTableInfo)
	r.PUT("/fetch/test", handle.GeneralSQLTest)
	r.GET("/fetch/detail", handle.GeneralOrderDetailList)
	r.GET("/fetch/roll", handle.GeneralOrderDetailRollSQL)
	r.POST("/fetch/rollorder", handle.RollBackSQLOrder)
	r.GET("/fetch/undo", handle.GeneralFetchUndo)
	r.PUT("/query/status", handle.FetchQueryStatus)
	r.POST("/query/refer", handle.ReferQueryOrder)
	r.PUT("/query/fetchbase", handle.FetchQueryDatabaseInfo)
	r.GET("/query/fetchtable/:t/:source", handle.FetchQueryTableInfo)
	r.GET("/query/tableinfo/:base/:table/:source", handle.FetchQueryTableStruct)
	r.POST("/query", handle.FetchQueryResults)
	r.DELETE("/query/undo", handle.UndoQueryOrder)
	r.PUT("/query/beauty", handle.GeneralQueryBeauty)
	r.PUT("/query/merge", handle.GeneralMergeDDL)
	r.POST("/sql/refer", handle.SQLReferToOrder)

	audit := r.Group("/audit", AuditGroup)
	audit.POST("/refer/perform", handle.MulitAuditOrder)
	audit.PUT("", handle.FetchAuditOrder)
	audit.GET("/sql", handle.FetchOrderSQL)
	audit.GET("/kill/:work_id", handle.DelayKill)
	audit.POST("/reject", handle.RejectOrder)
	audit.POST("/execute", handle.ExecuteOrder)
	audit.PUT("/record", handle.FetchRecord)
	audit.POST("/undo", handle.UndoAuditOrder)
	audit.PUT("/query/fetch", handle.FetchQueryOrder)
	audit.POST("/query/agreed", handle.AgreedQueryOrder)
	audit.POST("/query/disagreed", handle.DisAgreedQueryOrder)
	audit.POST("/query/undo", handle.SuperUndoQueryOrder)
	audit.PUT("/query/clear", handle.DelQueryOrder)
	audit.PUT("/query/cancel", handle.QueryQuickCancel)
	audit.PUT("/query/fetch/record", handle.FetchQueryRecord)
	audit.PUT("/query/fetch/record/detail", handle.FetchQueryRecordDetail)
	audit.GET("/fetch_osc/:work_id", handle.OscPercent)
	audit.DELETE("/fetch_osc/:work_id", handle.OscKill)

	group := r.Group("/group", SuperManageGroup)
	group.GET("", handle.SuperGroup)
	group.POST("/update", handle.SuperGroupUpdate)
	group.POST("/m/update", handle.SuperMGroupUpdate)
	group.DELETE("/del/:clear", handle.SuperDeleteGroup)
	group.GET("/setting", handle.SuperFetchSetting)
	group.POST("/setting/add", handle.SuperSaveSetting)
	group.POST("/setting/roles", handle.SuperSaveRoles)
	group.PUT("/setting/test/:el", handle.SuperTestSetting)

	user := r.Group("/management_user", SuperManageUser)
	user.POST("/modify", handle.SuperModifyUser)
	user.POST("/password_reset", handle.SuperChangePassword)
	user.GET("/fetch", handle.SuperFetchUser)
	user.DELETE("/del/:user", handle.SuperDeleteUser)
	user.POST("/register", handle.SuperUserRegister)

	db := r.Group("/management_db", SuperManageDB)
	db.GET("/fetch/", handle.SuperFetchDB)
	db.POST("/add", handle.SuperAddDB)
	db.PUT("/test", handle.SuperTestDBConnect)
	db.DELETE("/del/:source", handle.SuperDeleteDb)
	db.PUT("/edit", handle.SuperModifyDb)

	rules := r.Group("/rules", SuperManageGroup)
	rules.GET("", handle.FetchGroupOrder)
	rules.PUT("/reject", handle.RejectGroupOrder)
	rules.PUT("/allow", handle.AllowGroupOrder)

	autoTask := r.Group("/auto", SuperManageGroup)
	autoTask.GET("", handle.SuperFetchAutoTaskSource)
	autoTask.POST("", handle.SuperReferAutoTask)
	autoTask.PUT("/fetch", handle.SuperFetchAutoTaskList)
	autoTask.POST("/edit", handle.SuperEditAutoTask)
	autoTask.DELETE("/:id", handle.SuperDeleteAutoTask)
	autoTask.POST("/active", handle.SuperAutoTaskActivation)
}
