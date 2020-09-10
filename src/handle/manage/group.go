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

package manage

import (
	"Yearning-go/src/handle/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"net/http"
	"net/url"
)

type k struct {
	Username   string
	Permission model.PermissionList
	Tp         int
	Group      []string
}

type marge struct {
	User   string
	Group  []string
	IsShow bool
}

func SuperGroup(c yee.Context) (err error) {
	var pg int
	var r []model.CoreRoleGroup

	f := new(commom.PageInfo)
	if err = c.Bind(f); err != nil {
		return err
	}
	start, end := lib.Paging(f.Page, 10)
	var source []model.CoreDataSource
	var query []model.CoreDataSource
	var u []model.CoreAccount
	model.DB().Select("source").Where("is_query =? or is_query = ?", 0, 2).Find(&source)
	model.DB().Select("source").Where("is_query =? or is_query = ?", 1, 2).Find(&query)
	model.DB().Select("username").Where("rule in (?)", []string{"admin", "super"}).Find(&u)
	if f.Find.Valve {
		model.DB().Where("name LIKE ?", "%"+fmt.Sprintf("%s", f.Find.Text)+"%").Offset(start).Limit(end).Find(&r)
		model.DB().Where("name LIKE ?", "%"+fmt.Sprintf("%s", f.Find.Text)+"%").Model(model.CoreRoleGroup{}).Count(&pg)
	} else {
		model.DB().Offset(start).Limit(end).Find(&r)
		model.DB().Model(model.CoreRoleGroup{}).Count(&pg)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": r, "page": pg, "query": query, "common": u, "source": source})
}

func SuperGroupUpdate(c yee.Context) (err error) {
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
			}
			return c.JSON(http.StatusOK, fmt.Sprintf("%s权限组已创建！", u.Username))
		} else {
			g, _ := json.Marshal(u.Group)
			model.DB().Model(model.CoreGrained{}).Where("username = ?", u.Username).Updates(model.CoreGrained{Group: g})
			return c.JSON(http.StatusOK, fmt.Sprintf("%s的权限已更新！", u.Username))
		}
	}
	return c.JSON(http.StatusForbidden, "非法越权操作！")
}

func SuperClearUserRule(c yee.Context) (err error) {
	gx := c.Params("clear")
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
		per := lib.MulitUserRuleMarge(new)
		s_new, _ := json.Marshal(new)
		s_per, _ := json.Marshal(per)
		model.DB().Model(model.CoreGrained{}).Where("username =?", i.Username).Update(map[string]interface{}{"group": s_new, "permissions": s_per})
	}
	model.DB().Model(model.CoreRoleGroup{}).Where("`name` =?", g).Delete(&model.CoreRoleGroup{})
	return c.JSON(http.StatusOK, fmt.Sprintf("权限组: %s 已删除", g))
}

func SuperUserRuleMarge(c yee.Context) (err error) {
	u := new(marge)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	m3 := lib.MulitUserRuleMarge(u.Group)

	return c.JSON(http.StatusOK, m3)
}