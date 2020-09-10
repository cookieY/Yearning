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
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/cookieY/yee"
	"github.com/jinzhu/gorm"
)

type dbInfo struct {
	Source   string `json:"source"`
	IDC      string `json:"idc"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	IP       string `json:"ip"`
	Username string `json:"username"`
	IsQuery  int    `json:"is_query"`
	Valve    bool   `json:"valve"`
}

type editDb struct {
	Data dbInfo
}

func SuperTestDBConnect(c yee.Context) (err error) {

	u := new(dbInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	db, e := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/?charset=utf8&parseTime=True&loc=Local", u.Username, u.Password, u.IP, u.Port))
	defer func() {
		if err := db.Close(); err != nil {
			c.Logger().Error(err.Error())
		}
	}()
	if e != nil {
		return c.JSON(http.StatusOK, "数据库实例连接失败！请检查相关配置是否正确！")
	}
	return c.JSON(http.StatusOK, "数据库实例连接成功！")
}

func SuperFetchSource(c yee.Context) (err error) {

	var f dbInfo
	var u []model.CoreDataSource
	var pg int
	con := c.QueryParam("con")
	if err := json.Unmarshal([]byte(con), &f); err != nil {
		c.Logger().Error(err.Error())
	}
	start, end := lib.Paging(c.QueryParam("page"), 10)

	if f.Valve {
		model.DB().Model(model.CoreDataSource{}).Where("id_c LIKE ? and source LIKE ?", "%"+fmt.Sprintf("%s", f.IDC)+"%", "%"+fmt.Sprintf("%s", f.Source)+"%").Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&u)
	} else {
		model.DB().Model(model.CoreDataSource{}).Order("id desc").Count(&pg).Offset(start).Limit(end).Find(&u)
	}
	for idx := range u {
		u[idx].Password = "***********"
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"page": pg, "data": u, "custom": model.GloOther.IDC})
}

func SuperCreateSource(c yee.Context) (err error) {

	var refer model.CoreDataSource

	u := new(dbInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	if model.DB().Where("source =?", u.Source).First(&refer).RecordNotFound() {
		x := lib.Encrypt(u.Password)
		if x != "" {
			model.DB().Create(&model.CoreDataSource{
				IDC:      u.IDC,
				Source:   u.Source,
				Port:     u.Port,
				IP:       u.IP,
				Password: x,
				Username: u.Username,
				IsQuery:  u.IsQuery,
			})
			return c.JSON(http.StatusOK, "连接名添加成功！")
		}
		c.Logger().Error("config.toml文件中SecretKey值必须为16位！")
		return c.JSON(http.StatusInternalServerError, nil)
	} else {
		return c.JSON(http.StatusOK, "连接名称重复,请更改为其他!")
	}
}

func SuperDeleteSource(c yee.Context) (err error) {

	var k []model.CoreRoleGroup

	tx := model.DB().Begin()

	source := c.QueryParam("source")

	unescape, _ := url.QueryUnescape(source)

	model.DB().Find(&k)

	if er := tx.Where("source =?", unescape).Delete(&model.CoreDataSource{}).Error; er != nil {
		tx.Rollback()
		c.Logger().Error(er.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}

	for _, i := range k {
		var p model.PermissionList
		if err := json.Unmarshal(i.Permissions, &p); err != nil {
			c.Logger().Error(err.Error())
		}
		p.DDLSource = lib.ResearchDel(p.DDLSource, source)
		p.DMLSource = lib.ResearchDel(p.DMLSource, source)
		p.QuerySource = lib.ResearchDel(p.QuerySource, source)
		r, _ := json.Marshal(p)
		if e := tx.Model(&model.CoreRoleGroup{}).Where("id =?", i.ID).Update(model.CoreRoleGroup{Permissions: r}).Error; e != nil {
			tx.Rollback()
			c.Logger().Error(e.Error())
		}
	}

	tx.Model(model.CoreWorkflowTpl{}).Where("source =?", unescape).Delete(&model.CoreWorkflowTpl{})

	tx.Commit()
	return c.JSON(http.StatusOK, "数据库信息已删除")
}

func SuperEditSource(c yee.Context) (err error) {
	u := new(editDb)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, "")
	}
	if u.Data.Password == "***********" {
		model.DB().Model(&model.CoreDataSource{}).Where("source =?", u.Data.Source).Updates(&model.CoreDataSource{IP: u.Data.IP, Port: u.Data.Port, Username: u.Data.Username})
	} else {
		x := lib.Encrypt(u.Data.Password)
		model.DB().Model(&model.CoreDataSource{}).Where("source =?", u.Data.Source).Updates(&model.CoreDataSource{IP: u.Data.IP, Port: u.Data.Port, Username: u.Data.Username, Password: x})
	}

	return c.JSON(http.StatusOK, "数据源信息已更新!")
}

func ManageDbApi() yee.RestfulAPI {
	return yee.RestfulAPI{
		Get:    SuperFetchSource,
		Post:   SuperCreateSource,
		Delete: SuperDeleteSource,
		Put:    SuperEditSource,
	}
}
