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
	"Yearning-go/src/handle"
	"Yearning-go/src/handle/manage"
	"Yearning-go/src/handle/order"
	"Yearning-go/src/handle/post"
	"Yearning-go/src/handle/query"
	"Yearning-go/src/handle/record"
	"Yearning-go/src/handle/user"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"net/http"

	"github.com/cookieY/yee"
	"github.com/cookieY/yee/middleware"
)

func SuperManageGroup() yee.HandlerFunc {
	return func(c yee.Context) (err error) {
		_, role := lib.JwtParse(c)
		if role == "super" {
			return
		}
		return c.ServerError(http.StatusForbidden, "非法越权操作！")
	}
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

func TplGroup() yee.HandlerFunc {
	return func(c yee.Context) (err error) {
		_, rule := lib.JwtParse(c)
		if rule != "guest" || c.Request().Method == http.MethodPut {
			return
		}
		return c.ServerError(http.StatusForbidden, "非法越权操作！")
	}
}

func AddRouter(e *yee.Core) {

	e.GET("/", func(c yee.Context) error {
		return c.HTMLTml(http.StatusOK, "./dist/index.html")
	})

	e.POST("/login", user.UserGeneralLogin)
	e.POST("/register", user.UserRegister)
	e.GET("/fetch", user.UserReqSwitch)
	e.POST("/ldap", user.UserLdapLogin)

	r := e.Group("/api/v2", middleware.JWTWithConfig(middleware.JwtConfig{SigningKey: []byte(model.JWT)}))
	r.PUT("/user/edit/:tp", user.GeneralUserEdit)
	r.PUT("/user/order", order.GeneralFetchMyOrder)

	r.Restful("/dash/:tp",apis.YearningDashApis())
	r.Restful("/fetch/:tp", apis.YearningFetchApis())
	r.Restful("/query/:tp", apis.YearningQueryApis())

	r.POST("/sql/refer", post.SQLReferToOrder)
	r.GET("/board", handle.GeneralFetchBoard)
	r.GET("/steps", order.FetchStepsDetail)

	audit := r.Group("/audit", AuditGroup())
	audit.POST("/test", handle.SuperSQLTest)
	audit.POST("/agree", order.MultiAuditOrder)
	audit.PUT("", order.FetchAuditOrder)
	audit.GET("/sql", order.FetchOrderSQL)
	audit.GET("/kill/:work_id", order.DelayKill)
	audit.POST("/reject", order.RejectOrder)
	audit.POST("/execute", order.ExecuteOrder)
	audit.PUT("/record", record.FetchRecord)
	audit.PUT("/query/fetch", query.FetchQueryOrder)
	audit.POST("/query/handle/:tp", query.QueryHandlerSets)
	audit.DELETE("/query/empty", query.QueryDeleteEmptyRecord)
	audit.PUT("/query/fetch/record", record.FetchQueryRecord)
	audit.PUT("/query/fetch/record/detail", record.FetchQueryRecordDetail)
	audit.GET("/fetch_osc/:work_id", order.OscPercent)
	audit.DELETE("/fetch_osc/:work_id", order.OscKill)

	group := r.Group("/group", SuperManageGroup())
	group.PUT("", manage.SuperGroup)
	group.POST("/update", manage.SuperGroupUpdate)
	group.POST("/fetch/marge", manage.SuperUserRuleMarge)
	group.DELETE("/del/:clear", manage.SuperClearUserRule)
	group.GET("/setting", manage.SuperFetchSetting)
	group.POST("/setting/add", manage.SuperSaveSetting)
	group.POST("/setting/roles", manage.SuperSaveRoles)
	group.PUT("/setting/test/:el", manage.SuperTestSetting)
	group.POST("/setting/del/order", manage.UndoAuditOrder)
	group.POST("/setting/del/query", manage.DelQueryOrder)
	group.POST("/board/post", handle.GeneralPostBoard)

	managerUser := r.Group("/manage_user", SuperManageGroup())
	managerUser.Restful("", user.SuperUserApi())
	managerUser.GET("/depend", user.FetchUserDepend)
	managerUser.POST("/fetch/group", user.FetchUserPermissions)

	db := r.Group("/management_db", SuperManageGroup())
	db.Restful("", manage.ManageDbApi())
	db.PUT("/test", manage.SuperTestDBConnect)

	tpl := r.Group("/tpl", TplGroup())
	tpl.Restful("", manage.TplRestApis())

	autoTask := r.Group("/auto", SuperManageGroup())
	autoTask.POST("", manage.SuperReferAutoTask)
	autoTask.PUT("/fetch", manage.SuperFetchAutoTaskList)
	autoTask.POST("/edit", manage.SuperEditAutoTask)
	autoTask.DELETE("/:id", manage.SuperDeleteAutoTask)
	autoTask.POST("/active", manage.SuperAutoTaskActivation)
}
