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
	"Yearning-go/src/handler/order/osc"
	query2 "Yearning-go/src/handler/order/query"
	"Yearning-go/src/handler/personal"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee"
	"github.com/cookieY/yee/middleware"
	"github.com/gobuffalo/packr/v2"
	"log"
	"net/http"
	"os"
	"strings"
)

func SuperManageGroup() yee.HandlerFunc {
	return func(c yee.Context) (err error) {
		_, role := lib.JwtParse(c)
		if role == "super" || focalPoint(c) {
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

func AuditGroup() yee.HandlerFunc {
	return func(c yee.Context) (err error) {
		_, rule := lib.JwtParse(c)
		if rule != "guest" {
			return
		}
		return c.ServerError(http.StatusForbidden, "非法越权操作！")
	}
}

func AddRouter(e *yee.Core, box *packr.Box) {
	if os.Getenv("DEV") == "" {
		s, err := box.FindString("index.html")
		if err != nil {
			log.Fatal(err)
		}
		e.GET("/", func(c yee.Context) error {
			return c.HTML(http.StatusOK, s)
		})
	}
	e.POST("/login", login.UserGeneralLogin)
	e.POST("/register", login.UserRegister)
	e.GET("/fetch", login.UserReqSwitch)
	e.POST("/ldap", login.UserLdapLogin)

	r := e.Group("/api/v2", middleware.JWTWithConfig(middleware.JwtConfig{SigningKey: []byte(model.JWT)}))
	r.Restful("/common/:tp", personal.PersonalRestFulAPis())
	r.Restful("/dash/:tp", apis.YearningDashApis())
	r.Restful("/fetch/:tp", apis.YearningFetchApis())
	r.Restful("/query/:tp", apis.YearningQueryApis())

	audit := r.Group("/audit", AuditGroup())
	audit.Restful("/order/:tp", audit2.AuditRestFulAPis())
	audit.Restful("/osc/:work_id", osc.AuditOSCFetchStateApis())
	audit.Restful("/query/:tp", query2.AuditQueryRestFulAPis())

	manager := r.Group("/manage", SuperManageGroup())
	manager.POST("/board/post", manage.GeneralPostBoard)

	db := manager.Group("/db")
	db.Restful("", db2.ManageDbApi())

	account := manager.Group("/user")
	account.Restful("", user2.SuperUserApi())

	tpl := manager.Group("/tpl")
	tpl.Restful("", tpl2.TplRestApis())

	group := manager.Group("/group")
	group.Restful("", group2.GroupsApis())

	setting := manager.Group("/setting")
	setting.Restful("", settings.SettingsApis())

	roles := manager.Group("/roles")
	roles.Restful("", roles2.RolesApis())

	autoTask := manager.Group("/task")
	autoTask.Restful("", autoTask2.SuperAutoTaskApis())
}
