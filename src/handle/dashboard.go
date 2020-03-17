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

package handle

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/labstack/echo/v4"
	"net/http"
)

type groupBy struct {
	DataBase string
	C        int
	Time     string
}

type wkid struct {
	WorkId     string
	Username   string
	Permission model.PermissionList
}

func DashInit(c echo.Context) (err error) {
	var permissionList model.CoreGrained
	var super map[string]string
	user, _ := lib.JwtParse(c)
	model.DB().Select("permissions").Where("username =?", user).First(&permissionList)
	if user == "admin" {
		super = map[string]string{"group": "1", "setting": "1", "perOrder": "1", "roles": "1", "task": "1", "roleGroup": "1"}
	} else {
		super = map[string]string{"group": "0", "setting": "0", "perOrder": "0", "roles": "0"}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"c": permissionList.Permissions, "s": super})
}

func DashCount(c echo.Context) (err error) {
	var (
		userCount   int
		orderCount  int
		queryCount  int
		sourceCount int
		s           []groupBy
	)
	model.DB().Table("core_sql_orders").Select("data_base, count(*) as c").Group("data_base").Order("c desc").Limit(5).Scan(&s)
	model.DB().Model(&model.CoreAccount{}).Count(&userCount)
	model.DB().Model(&model.CoreQueryOrder{}).Select("id").Count(&queryCount)
	model.DB().Model(&model.CoreSqlOrder{}).Select("id").Count(&orderCount)
	model.DB().Model(&model.CoreDataSource{}).Select("id").Count(&sourceCount)

	return c.JSON(http.StatusOK, map[string]interface{}{"createUser": userCount, "order": orderCount, "source": sourceCount, "query":queryCount , "dataTop5": s})
}

func DashUserInfo(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	var (
		u         model.CoreAccount
		p         model.CoreGrained
		groupList []model.CoreRoleGroup
		s         model.CoreGlobalConfiguration
	)
	model.DB().Select("username,rule,department,real_name,email").Where("username =?", user).Find(&u)
	model.DB().Select("`group`").Where("username =?", user).First(&p)
	model.DB().Select("`name`").First(&groupList)
	model.DB().Select("stmt").First(&s)
	return c.JSON(http.StatusOK, map[string]interface{}{"u": u, "p": p.Group, "s": s,"g":groupList})
}

func DashStmt(c echo.Context) (err error) {
	model.DB().Model(&model.CoreGlobalConfiguration{}).Where("authorization =?", "global").Update("stmt", 1)
	return c.JSON(http.StatusOK, "")
}

func DashPie(c echo.Context) (err error) {
	var (
		queryCount int
		ddlCount   int
		dmlCount   int
	)
	model.DB().Model(&model.CoreQueryOrder{}).Select("id").Count(&queryCount)
	model.DB().Model(&model.CoreSqlOrder{}).Where("`type` =? ", 1).Count(&dmlCount)
	model.DB().Model(&model.CoreSqlOrder{}).Where("`type` =? ", 0).Count(&ddlCount)
	return c.JSON(http.StatusOK, map[string]int{"ddl": ddlCount, "dml": dmlCount, "query": queryCount})
}

func DashAxis(c echo.Context) (err error) {
	var ddl []groupBy
	var order []int
	var count []string
	model.DB().Table("core_sql_orders").Select("time, count(*) as c").Group("time").Order("time desc").Limit(7).Scan(&ddl)

	for _, i := range ddl {
		order = append(order, i.C)
		count = append(count, i.Time)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"o": order, "c": count})
}
