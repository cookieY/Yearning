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
	"Yearning-go/src/apis"
	"Yearning-go/src/handler/login"
	"Yearning-go/src/handler/manage"
	autoTask2 "Yearning-go/src/handler/manage/autoTask"
	db2 "Yearning-go/src/handler/manage/db"
	group2 "Yearning-go/src/handler/manage/group"
	roles2 "Yearning-go/src/handler/manage/roles"
	"Yearning-go/src/handler/manage/settings"
	tpl2 "Yearning-go/src/handler/manage/tpl"
	user2 "Yearning-go/src/handler/manage/user"
	audit2 "Yearning-go/src/handler/order/audit"
	query2 "Yearning-go/src/handler/order/query"
	"Yearning-go/src/handler/order/record"
	"Yearning-go/src/handler/personal"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"net/http"
	"strings"

	"github.com/cookieY/yee"
	"github.com/cookieY/yee/middleware"
)

func SuperManageGroup() yee.HandlerFunc {
	return func(c yee.Context) (err error) {
		role := new(lib.Token).JwtParse(c)
		if role.Username == "admin" || focalPoint(c) {
			return
		}
		c.Abort()
		return c.JSON(http.StatusForbidden, "非法越权操作！")
	}
}

func SuperRecorderGroup() yee.HandlerFunc {
	return func(c yee.Context) (err error) {
		if c.IsWebsocket() {
			return nil
		}
		role := new(lib.Token).JwtParse(c)
		if role.IsRecord {
			return
		}
		return c.ServerError(http.StatusForbidden, "非法越权操作！")
	}
}

func focalPoint(c yee.Context) bool {

	if strings.Contains(c.RequestURI(), "/api/v2/manage/tpl") && c.Request().Method == http.MethodPut {
		return true
	}

	if strings.Contains(c.RequestURI(), "/api/v2/manage/group") && c.Request().Method == http.MethodGet {
		return true
	}
	return false
}

func AddRouter(e *yee.Core) {
	e.POST("/login", login.UserGeneralLogin)
	e.POST("/register", login.UserRegister)
	e.GET("/fetch", login.UserReqSwitch)
	e.GET("/lang", login.SystemLang)
	e.POST("/ldap", login.UserLdapLogin)
	e.GET("/oidc/_token-login", login.OidcLogin)
	e.GET("/oidc/state", login.OidcState)

	r := e.Group("/api/v2", middleware.JWTWithConfig(middleware.JwtConfig{SigningKey: []byte(model.JWT)}))
	r.Restful("/common/:tp", personal.PersonalRestFulAPis())
	r.Restful("/dash/:tp", apis.YearningDashApis())
	r.Restful("/fetch/:tp", apis.YearningFetchApis())
	r.Restful("/query/:tp", apis.YearningQueryApis())
	r.GET("/board/get", manage.GeneralGetBoard)

	audit := r.Group("/audit")
	audit.Restful("/order/:tp", audit2.AuditRestFulAPis())
	//audit.Restful("/osc/:work_id", osc.AuditOSCFetchStateApis())
	audit.Restful("/query/:tp", query2.AuditQueryRestFulAPis())

	re := r.Group("/record", SuperRecorderGroup())
	re.GET("/axis", record.RecordDashAxis)
	re.GET("/list", record.RecordOrderList)

	manager := r.Group("/manage", SuperManageGroup())
	manager.POST("/board/post", manage.GeneralPostBoard)
	manager.GET("/board/get", manage.GeneralGetBoard)

	db := manager.Group("/db")
	db.Restful("", db2.ManageDbApi())

	account := manager.Group("/user")
	account.Restful("", user2.SuperUserApi())

	tpl := manager.Group("/tpl")
	tpl.Restful("", tpl2.TplRestApis())

	group := manager.Group("/policy")
	group.Restful("", group2.GroupsApis())
	group.GET("/source", group2.SuperGetRuseSource)

	setting := manager.Group("/setting")
	setting.Restful("", settings.SettingsApis())

	roles := manager.Group("/roles/:tp")
	roles.Restful("", roles2.RolesApis())

	autoTask := manager.Group("/task")
	autoTask.Restful("", autoTask2.SuperAutoTaskApis())
}
