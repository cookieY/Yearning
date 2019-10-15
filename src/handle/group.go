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
)

type s struct {
	Data   []model.CoreGrained    `json:"data"`
	Page   int                    `json:"page"`
	Source []model.CoreDataSource `json:"source"`
	Audit  []model.CoreAccount    `json:"audit"`
	Query  []model.CoreDataSource `json:"query"`
}

type k struct {
	Username   string
	Permission model.PermissionList
}

type m struct {
	Username   []string
	Permission model.PermissionList
}

func SuperGroup(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	if user == "admin" {
		var f fetchdb
		var pg int
		var g []model.CoreGrained
		var source []model.CoreDataSource
		var query []model.CoreDataSource
		var u []model.CoreAccount
		con := c.QueryParam("con")
		start, end := lib.Paging(c.QueryParam("page"), 10)

		if err := json.Unmarshal([]byte(con), &f); err != nil {
			c.Logger().Error(err.Error())
		}
		if f.Valve {
			model.DB().Where("username LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%").Offset(start).Limit(end).Find(&g)
			model.DB().Where("username LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%").Model(model.CoreGrained{}).Count(&pg)
		} else {
			model.DB().Offset(start).Limit(end).Find(&g)
			model.DB().Model(model.CoreGrained{}).Count(&pg)
		}

		model.DB().Select("source").Where("is_query =? or is_query = ?", 0, 2).Find(&source)
		model.DB().Select("source").Where("is_query =? or is_query = ?", 1, 2).Find(&query)
		model.DB().Select("username").Where("rule =?", "admin").Find(&u)
		return c.JSON(http.StatusOK, s{Data: g, Page: pg, Source: source, Audit: u, Query: query})
	}
	return c.JSON(http.StatusForbidden, "非法越权操作！")
}

func SuperGroupUpdate(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	if user == "admin" {
		u := new(k)
		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}
		g, err := json.Marshal(u.Permission)
		if err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}
		model.DB().Model(model.CoreGrained{}).Where("username = ?", u.Username).Update(model.CoreGrained{Permissions: g})
		return c.JSON(http.StatusOK, fmt.Sprintf("用户:%s 权限已更新！", u.Username))
	}
	return c.JSON(http.StatusForbidden, "非法越权操作！")
}

func SuperMGroupUpdate(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)

	if user == "admin" {

		u := new(m)

		if err = c.Bind(u); err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}

		g, err := json.Marshal(u.Permission)
		if err != nil {
			c.Logger().Error(err.Error())
			return c.JSON(http.StatusInternalServerError, err.Error())
		}

		for _, i := range u.Username {
			model.DB().Model(model.CoreGrained{}).Where("username = ?", i).Update(model.CoreGrained{Permissions: g})
		}

		return c.JSON(http.StatusOK, "用户权限已更新！")
	}
	return c.JSON(http.StatusForbidden, "非法越权操作！")
}

func SuperDeleteGroup(c echo.Context) (err error) {
	g := c.Param("clear")
	u, err := json.Marshal(model.InitPer)
	model.DB().Model(model.CoreGrained{}).Where("username =?", g).Update(model.CoreGrained{Permissions: u})
	return c.JSON(http.StatusOK, fmt.Sprintf("用户:%s 权限已清空！", g))
}
