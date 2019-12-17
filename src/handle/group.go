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
	"net/url"
	"strings"
)

type s struct {
	Data   []model.CoreGrained    `json:"data"`
	Data2  []model.CoreRoleGroup  `json:"data2"`
	Page   int                    `json:"page"`
	Source []model.CoreDataSource `json:"source"`
	Audit  []model.CoreAccount    `json:"audit"`
	Query  []model.CoreDataSource `json:"query"`
}

type k struct {
	Username   string
	Permission model.PermissionList
	Tp         int
	Group      []string
}

type m struct {
	Username   []string
	Permission model.PermissionList
}

type marge struct {
	User  string
	Group []string
}

func SuperGroup(c echo.Context) (err error) {
	user, _ := lib.JwtParse(c)
	if user == "admin" {
		var f fetchdb
		var pg int
		var g []model.CoreGrained
		var r []model.CoreRoleGroup

		con := c.QueryParam("con")
		tp := c.QueryParam("tp")
		start, end := lib.Paging(c.QueryParam("page"), 10)

		if err := json.Unmarshal([]byte(con), &f); err != nil {
			c.Logger().Error(err.Error())
		}

		if tp == "1" {
			var source []model.CoreDataSource
			var query []model.CoreDataSource
			var u []model.CoreAccount
			model.DB().Select("source").Where("is_query =? or is_query = ?", 0, 2).Find(&source)
			model.DB().Select("source").Where("is_query =? or is_query = ?", 1, 2).Find(&query)
			model.DB().Select("username").Where("rule =?", "admin").Find(&u)
			if f.Valve {
				model.DB().Where("name LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%").Offset(start).Limit(end).Find(&r)
				model.DB().Where("name LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%").Model(model.CoreRoleGroup{}).Count(&pg)
			} else {
				model.DB().Offset(start).Limit(end).Find(&r)
				model.DB().Model(model.CoreRoleGroup{}).Count(&pg)
			}
			return c.JSON(http.StatusOK, s{Data2: r, Page: pg, Source: source, Audit: u, Query: query,})
		} else {
			if f.Valve {
				model.DB().Select("id,username,rule,`group`").Where("username LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%").Offset(start).Limit(end).Find(&g)
				model.DB().Select("id,username,rule,`group`").Where("username LIKE ?", "%"+fmt.Sprintf("%s", f.Username)+"%").Model(model.CoreGrained{}).Count(&pg)
			} else {
				model.DB().Select("id,username,rule,`group`").Offset(start).Limit(end).Find(&g)
				model.DB().Model(model.CoreGrained{}).Count(&pg)
			}
			model.DB().Select("name").Find(&r)
			return c.JSON(http.StatusOK, s{Data: g, Page: pg, Data2: r})
		}
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
		if u.Tp == 1 {
			var s model.CoreRoleGroup
			if model.DB().Where("`name` =?", u.Username).First(&s).RecordNotFound() {
				model.DB().Create(&model.CoreRoleGroup{
					Name:        u.Username,
					Permissions: g,
				})
			} else {
				model.DB().Model(model.CoreRoleGroup{}).Where("`name` =?", u.Username).Update(&model.CoreRoleGroup{Permissions: g})
				var j []model.CoreGrained
				w := "%" + u.Username + "%"
				model.DB().Where("`group` like ?", w).Find(&j)
				for _, i := range j {
					var m1 []string
					_ = json.Unmarshal(i.Group, &m1)
					per, _ := json.Marshal(MulitUserRuleMarge(m1))
					model.DB().Model(model.CoreGrained{}).Where("`username` =?", i.Username).Update(&model.CoreGrained{Permissions: per})
				}
			}
			return c.JSON(http.StatusOK, fmt.Sprintf("%s权限组已创建！", u.Username))
		} else {
			t, _ := json.Marshal(u.Permission)
			g, _ := json.Marshal(u.Group)
			model.DB().Model(model.CoreGrained{}).Where("username = ?", u.Username).Updates(model.CoreGrained{Permissions: t, Group: g})
			return c.JSON(http.StatusOK, fmt.Sprintf("用户:%s 权限已更新！", u.Username))
		}
	}
	return c.JSON(http.StatusForbidden, "非法越权操作！")
}

func SuperClearUserRule(c echo.Context) (err error) {
	gx := c.Param("clear")
	g, _ := url.QueryUnescape(gx)
	var j []model.CoreGrained
	var k model.CoreRoleGroup
	w := "%" + g + "%"
	model.DB().Where("`group` like ?", w).Find(&j)
	model.DB().Where("`name` =?", lib.GenWorkid()).First(&k)
	var m2 model.PermissionList
	_ = json.Unmarshal(k.Permissions, &m2)
	for _, i := range j {
		var m1 []string
		_ = json.Unmarshal(i.Group, &m1)
		new := lib.ResearchDel(m1, g)
		per := MulitUserRuleMarge(new)
		s_new, _ := json.Marshal(new)
		s_per, _ := json.Marshal(per)
		model.DB().Model(model.CoreGrained{}).Where("username =?", i.Username).Update(map[string]interface{}{"group": s_new, "permissions": s_per})
	}
	model.DB().Model(model.CoreRoleGroup{}).Where("`name` =?", g).Delete(&model.CoreRoleGroup{})
	return c.JSON(http.StatusOK, fmt.Sprintf("权限组: %s 已删除", g))
}

func SuperUserRuleMarge(c echo.Context) (err error) {
	u := new(marge)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	m3 := MulitUserRuleMarge(u.Group)
	return c.JSON(http.StatusOK, m3)
}

func MulitUserRuleMarge(group []string) model.PermissionList {
	var u model.PermissionList
	for _, i := range group {
		var k model.CoreRoleGroup
		model.DB().Where("name =?", i).First(&k)
		var m1 model.PermissionList
		_ = json.Unmarshal(k.Permissions, &m1)
		u.DDL += m1.DDL
		u.DML += m1.DML
		u.Base += m1.Base
		u.Query += m1.Query
		u.User += m1.User
		u.DDLSource = append(u.DDLSource, m1.DDLSource...)
		u.DMLSource = append(u.DMLSource, m1.DMLSource...)
		u.QuerySource = append(u.QuerySource, m1.QuerySource...)
		u.Auditor = append(u.Auditor, m1.Auditor...)
	}
	if strings.Contains(u.DDL, "1") {
		u.DDL = "1"
	} else {
		u.DDL = "0"
	}
	if strings.Contains(u.DML, "1") {
		u.DML = "1"
	} else {
		u.DML = "0"
	}
	if strings.Contains(u.User, "1") {
		u.User = "1"
	} else {
		u.User = "0"
	}
	if strings.Contains(u.Base, "1") {
		u.Base = "1"
	} else {
		u.Base = "0"
	}
	if strings.Contains(u.Query, "1") {
		u.Query = "1"
	} else {
		u.Query = "0"
	}

	u.DDLSource = removeDuplicateElement(u.DDLSource)
	u.DMLSource = removeDuplicateElement(u.DMLSource)
	u.Auditor = removeDuplicateElement(u.Auditor)
	u.QuerySource = removeDuplicateElement(u.QuerySource)

	return u
}

func removeDuplicateElement(addrs []string) []string {
	result := make([]string, 0, len(addrs))
	temp := map[string]struct{}{}
	for _, item := range addrs {
		if _, ok := temp[item]; !ok {
			temp[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}
