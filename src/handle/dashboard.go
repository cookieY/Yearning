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
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"net/http"
	"time"
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
		super = map[string]string{"group": "1", "setting": "1", "perOrder": "1", "roles": "1","task": "1"}
	} else {
		super = map[string]string{"group": "0", "setting": "0", "perOrder": "0","roles": "0"}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"c": permissionList.Permissions, "s": super})
}

func DashCount(c echo.Context) (err error) {
	var userCount int
	var orderCount int
	var queryCount int
	var sourceCount int
	var s []groupBy
	model.DB().Table("core_sql_orders").Select("data_base, count(*) as c").Group("data_base").Order("c desc").Limit(5).Scan(&s)
	model.DB().Model(&model.CoreAccount{}).Count(&userCount)
	model.DB().Model(&model.CoreQueryOrder{}).Select("id").Count(&queryCount)
	model.DB().Model(&model.CoreSqlOrder{}).Select("id").Count(&orderCount)
	model.DB().Model(&model.CoreDataSource{}).Select("id").Count(&sourceCount)

	return c.JSON(http.StatusOK, map[string]interface{}{"u": userCount, "o": orderCount, "q": queryCount, "s": sourceCount, "top": s})
}

func DashUserInfo(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	var u model.CoreAccount
	var au []model.CoreAccount
	var p model.CoreGrained
	var s model.CoreGlobalConfiguration
	var source []model.CoreDataSource
	var pu model.JSON
	var query []model.CoreDataSource
	model.DB().Select("username,rule,department,real_name,email").Where("username =?", user).Find(&u)
	model.DB().Select("permissions").Where("username =?", user).First(&p)
	model.DB().Select("stmt").First(&s)
	model.DB().Select("username").Where("rule =?", "admin").Find(&au)
	model.DB().Select("source").Where("is_query =? or is_query = ?", 0, 2).Find(&source)
	model.DB().Select("source").Where("is_query =? or is_query = ?", 1, 2).Find(&query)
	if err := json.Unmarshal(p.Permissions, &pu); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"u": u, "p": pu, "s": s, "source": source, "au": au, "query": query})
}

func DashStmt(c echo.Context) (err error) {
	model.DB().Model(&model.CoreGlobalConfiguration{}).Where("authorization =?", "global").Update("stmt", 1)
	return c.JSON(http.StatusOK, "")
}

func DashPie(c echo.Context) (err error) {
	var queryCount int
	var ddlCount int
	var dmlCount int
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
func ReferGroupOrder(c echo.Context) (err error) {
	u := new(wkid)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	var t groupBy
	var tv model.CoreGroupOrder
	user, _ := lib.JwtParse(c)
	model.DB().Model(model.CoreGroupOrder{}).Select("count(*) as c").Where("date =? AND username =?", time.Now().Format("2006-01-02"), user).Group("date").Scan(&t)
	if t.C > model.GloOther.PerOrder {
		return c.JSON(http.StatusOK, fmt.Sprintf("权限申请已达每日最大上限%d/次,请联系管理员！", model.GloOther.PerOrder))
	}

	model.DB().Model(model.CoreGroupOrder{}).Where("username =?",user).Last(&tv)
	if tv.Status == 2 {
		return c.JSON(http.StatusOK, "在上一次申请没有审核前,请勿重复提交！")
	}

	tk, _ := json.Marshal(u.Permission)
	wk := lib.GenWorkid()
	model.DB().Create(&model.CoreGroupOrder{
		WorkId:      wk,
		Permissions: tk,
		Date:        time.Now().Format("2006-01-02"),
		Status:      2,
		Username:    user,
	})
	lib.MessagePush(c, wk, 9, "")
	return c.JSON(http.StatusOK, "权限申请已提交")
}

func RejectGroupOrder(c echo.Context) (err error) {
	u := new(wkid)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	model.DB().Model(&model.CoreGroupOrder{}).Where("work_id =?", u.WorkId).Update("status", 0)
	lib.MessagePush(c, u.WorkId, 11, "")
	return c.JSON(http.StatusOK, "权限申请已驳回")
}

func AllowGroupOrder(c echo.Context) (err error) {
	u := new(wkid)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	var userPer model.CoreGroupOrder
	model.DB().Model(&model.CoreGroupOrder{}).Where("work_id =?", u.WorkId).Update("status", 1)
	model.DB().Select("username").Where("work_id =?", u.WorkId).First(&userPer)
	ix, _ := json.Marshal(u.Permission)
	model.DB().Model(&model.CoreGrained{}).Where("username =?", userPer.Username).Update(model.CoreGrained{Permissions: ix})
	lib.MessagePush(c, u.WorkId, 10, "")
	return c.JSON(http.StatusOK, "权限申请已通过")
}

func FetchGroupOrder(c echo.Context) (err error) {
	start, end := lib.Paging(c.QueryParam("page"), 20)
	var userPer []model.CoreGroupOrder
	var pg int
	model.DB().Offset(start).Limit(end).Order("id desc").Find(&userPer)
	model.DB().Model(&model.CoreGroupOrder{}).Count(&pg)
	return c.JSON(http.StatusOK, map[string]interface{}{"data": userPer, "pg": pg})
}
