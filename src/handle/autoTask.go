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

type autoTask struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Database string `json:"database"`
	Table    string `json:"table"`
	Tp       int    `json:"tp"`
	Row      uint   `json:"row"`
	Id       int    `json:"id"`
	Status   int    `json:"status"`
}

type fetchAutoTask struct {
	Tp autoTask
}

func SuperReferAutoTask(c echo.Context) (err error) {
	u := new(fetchAutoTask)
	var tmp model.CoreAutoTask
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	if model.DB().Model(model.CoreAutoTask{}).Where("`name` =? and source = ? and `table` =? and base =? and tp =?", u.Tp.Name, u.Tp.Source, u.Tp.Table, u.Tp.Database, u.Tp.Tp).First(&tmp).RecordNotFound() {
		model.DB().Create(&model.CoreAutoTask{
			Source:    u.Tp.Source,
			Base:      u.Tp.Database,
			Table:     u.Tp.Table,
			Tp:        u.Tp.Tp,
			Name:      u.Tp.Name,
			Affectrow: u.Tp.Row,
			Status:    0,
		})
		return c.JSON(http.StatusOK, "已添加autoTask任务!")
	} else {
		return c.JSON(http.StatusOK, "请勿重复添加相同名称的任务!")
	}
}

func SuperFetchAutoTaskSource(c echo.Context) (err error) {
	var source []model.CoreDataSource
	model.DB().Select("source").Where("is_query =? or is_query = ?", 0, 2).Find(&source)
	return c.JSON(http.StatusOK, source)
}
func SuperFetchAutoTaskList(c echo.Context) (err error) {
	u := new(f)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	var task []model.CoreAutoTask
	var pg int
	start, end := lib.Paging(u.Page, 15)
	if u.Find.Valve {
		model.DB().Where("name like ?", "%"+u.Find.Text+"%").Order("id desc").Offset(start).Limit(end).Find(&task)
		model.DB().Where("name like ?", "%"+u.Find.Text+"%").Model(&model.CoreAutoTask{}).Count(&pg)
	} else {
		model.DB().Order("id desc").Offset(start).Limit(end).Find(&task)
		model.DB().Model(&model.CoreAutoTask{}).Count(&pg)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": task, "pg": pg})
}

func SuperEditAutoTask(c echo.Context) (err error) {
	u := new(fetchAutoTask)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", u.Tp.Id).Update(&model.CoreAutoTask{
		Source:    u.Tp.Source,
		Base:      u.Tp.Database,
		Table:     u.Tp.Table,
		Tp:        u.Tp.Tp,
		Name:      u.Tp.Name,
		Affectrow: u.Tp.Row,
	})
	return c.JSON(http.StatusOK, "AutoTask信息已变更！")
}

func SuperDeleteAutoTask(c echo.Context) (err error) {
	id := c.Param("id")
	model.DB().Where("id =?", id).Delete(&model.CoreAutoTask{})
	return c.JSON(http.StatusOK, "AutoTask工单已删除！")
}

func SuperAutoTaskActivation(c echo.Context) (err error) {
	u := new(fetchAutoTask)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusInternalServerError, err.Error())
	}
	model.DB().Model(model.CoreAutoTask{}).Where("id =?", u.Tp.Id).Update("status",u.Tp.Status)
	return c.JSON(http.StatusOK,"AutoTask工单状态已变更！")
}
