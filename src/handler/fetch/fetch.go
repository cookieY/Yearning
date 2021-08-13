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

package fetch

import (
	"Yearning-go/src/handler/commom"
	tpl2 "Yearning-go/src/handler/manager/tpl"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"Yearning-go/src/soar"
	"encoding/json"
	"errors"
	"github.com/cookieY/yee"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
)

func FetchIDC(c yee.Context) (err error) {
	return c.JSON(http.StatusOK, commom.SuccessPayload(model.GloOther.IDC))

}

func FetchSource(c yee.Context) (err error) {

	u := new(_FetchBind)
	if err := c.Bind(u); err != nil {
		return err
	}
	if reflect.DeepEqual(u, _FetchBind{}) {
		return
	}

	unescape, _ := url.QueryUnescape(u.IDC)

	var s model.CoreGrained
	var groups []string
	var sList []string
	var source []model.CoreDataSource
	var inter []string
	var queryAuditor []string

	user, _ := lib.JwtParse(c)

	model.DB().Where("username =?", user).First(&s)

	if err := json.Unmarshal(s.Group, &groups); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	p := lib.MultiUserRuleMarge(groups)

	model.DB().Select("source").Where("id_c =?", unescape).Find(&source)

	var tpl model.CoreWorkflowTpl

	var whoIsAuditor []tpl2.Tpl

	if model.DB().Model(model.CoreWorkflowTpl{}).Where("source =?", unescape).Find(&tpl).RecordNotFound() {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(errors.New("环境没有添加流程!无法提交工单")))
	}
	_ = json.Unmarshal(tpl.Steps, &whoIsAuditor)

	queryAuditor = whoIsAuditor[1].Auditor

	if source != nil {
		for _, i := range source {
			sList = append(sList, i.Source)
		}
		switch u.Tp {
		case "dml":
			inter = lib.Intersect(p.DMLSource, sList)
		case "ddl":
			inter = lib.Intersect(p.DDLSource, sList)
		case "query":
			inter = lib.Intersect(p.QuerySource, sList)
			queryAuditor = p.Auditor
		case "all":
			inter = sList
		}
	}

	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"assigned": queryAuditor, "source": inter}))
}

func FetchBase(c yee.Context) (err error) {

	u := new(_FetchBind)
	if err := c.Bind(u); err != nil {
		return err
	}
	if reflect.DeepEqual(u, _FetchBind{}) {
		return
	}
	var s model.CoreDataSource

	var mid []string

	unescape, _ := url.QueryUnescape(u.Source)

	model.DB().Where("source =?", unescape).First(&s)

	result, err := commom.ScanDataRows(s, "", "SHOW DATABASES;", "库名", false)

	if err != nil {
		c.Logger().Error(err.Error())
		return
	}

	if len(model.GloOther.ExcludeDbList) > 0 {
		mid = lib.Intersect(result.Results, model.GloOther.ExcludeDbList)
		result.Results = lib.NonIntersect(mid, result.Results)
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"results": result.Results, "highlight": result.Highlight}))
}

func FetchTable(c yee.Context) (err error) {
	u := new(_FetchBind)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var s model.CoreDataSource

	model.DB().Where("source =?", u.Source).First(&s)

	result, err := commom.ScanDataRows(s, u.DataBase, "SHOW TABLES;", "表名", false)

	if err != nil {
		c.Logger().Error(err.Error())
		return
	}

	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"table": result.Results, "highlight": result.Highlight}))
}

func FetchTableInfo(c yee.Context) (err error) {
	u := new(_FetchBind)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	if err := u.FetchTableFieldsOrIndexes(); err != nil {
		c.Logger().Critical(err.Error())
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"rows": u.Rows, "idx": u.Idx}))
}

func FetchSQLTest(c yee.Context) (err error) {
	u := new(commom.SQLTest)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var s model.CoreDataSource
	model.DB().Where("source =?", u.Source).First(&s)
	ps := lib.Decrypt(s.Password)
	y := pb.LibraAuditOrder{
		IsDML:    u.IsDML,
		SQL:      u.SQL,
		DataBase: u.Database,
		Source: &pb.Source{
			Addr:     s.IP,
			User:     s.Username,
			Port:     int32(s.Port),
			Password: ps,
		},
		Execute: false,
		Check:   true,
	}
	record, err := lib.TsClient(&y)
	if err != nil {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(record))
}

func FetchOrderDetailList(c yee.Context) (err error) {
	workId := c.QueryParam("work_id")
	var record []model.CoreSqlRecord
	var count int
	start, end := lib.Paging(c.QueryParam("page"), 10)
	model.DB().Model(&model.CoreSqlRecord{}).Where("work_id =?", workId).Count(&count).Offset(start).Limit(end).Find(&record)
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"record": record, "count": count}))
}

func FetchOrderDetailRollSQL(c yee.Context) (err error) {
	workId := c.QueryParam("work_id")
	start, end := lib.Paging(c.QueryParam("page"), 5)
	var roll []model.CoreRollback
	var count int
	model.DB().Select("`sql`").Model(model.CoreRollback{}).Where("work_id =?", workId).Count(&count).Offset(start).Limit(end).Find(&roll)
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"sql": roll, "count": count}))
}

func FetchUndo(c yee.Context) (err error) {
	u := c.QueryParam("work_id")
	user, _ := lib.JwtParse(c)
	var undo model.CoreSqlOrder
	if model.DB().Where(UNDO_EXPR, user, u, 2).First(&undo).RecordNotFound() {
		return c.JSON(http.StatusOK, UNDO_MESSAGE_ERROR)
	}
	lib.MessagePush(undo.WorkId, 6, "")
	model.DB().Where(UNDO_EXPR, user, u, 2).Delete(&model.CoreSqlOrder{})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(UNDO_MESSAGE_SUCCESS))
}

func FetchMergeDDL(c yee.Context) (err error) {
	req := new(referOrder)
	if err = c.Bind(req); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}
	m, err := soar.MergeAlterTables(req.SQLs)
	if err != nil {
		return c.JSON(http.StatusOK, commom.ERR_SOAR_ALTER_MERGE(err))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(m))
}

func FetchSQLInfo(c yee.Context) (err error) {
	workId := c.QueryParam("work_id")
	limit := c.QueryParam("limit")
	var sql model.CoreSqlOrder
	model.DB().Select("`sql`").Where("work_id =?", workId).First(&sql)
	realSQL := sql.SQL
	if limit == "10" {
		tmp := strings.Split(sql.SQL, ";")
		if len(tmp) > 10 {
			realSQL = strings.Join(tmp[:9], "")
		}
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"sqls": realSQL}))
}

func FetchPerformList(c yee.Context) (err error) { // 获取审核人范围
	var user []model.CoreAccount
	model.DB().Scopes(commom.AccordingToRuleSuperOrAdmin()).Find(&user)
	return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"perform": user}))
}

// RollBackSQLOrder create order record if order type of rollback
func RollBackSQLOrder(c yee.Context) (err error) {
	u := new(referOrder)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	auditor := FetchTplAuditor(u.Data.IDC)
	if auditor == nil {
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(AUDITOR_IS_NOT_EXIST))
	}

	var sql strings.Builder
	if u.Tp != 1 {
		sql.WriteString(u.SQLs)
	} else {
		var roll []model.CoreRollback
		model.DB().Select("`sql`").Where("work_id =?", u.Data.WorkId).Find(&roll)
		for _, i := range roll {
			sql.WriteString(i.SQL)
			sql.WriteString("\n")
		}
	}
	w := lib.GenWorkid()
	u.Data.WorkId = w
	u.Data.Status = 2
	u.Data.Date = time.Now().Format("2006-01-02 15:04")
	u.Data.SQL = sql.String()
	u.Data.CurrentStep = 1
	u.Data.Time = time.Now().Format("2006-01-02")
	u.Data.Relevant = lib.JsonStringify([]string{auditor[0]})
	u.Data.Assigned = auditor[0]
	model.DB().Model(model.CoreSqlOrder{}).Create(&u.Data)
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   w,
		Username: u.Data.Username,
		Action:   "已提交",
		Rejected: "",
		Time:     time.Now().Format("2006-01-02 15:04"),
	})
	lib.MessagePush(w, 2, "")
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(commom.ORDER_IS_CREATE))
}

func FetchStepsProfile(c yee.Context) (err error) {
	workId := c.QueryParam("work_id")
	var s []model.CoreWorkflowDetail
	model.DB().Where("work_id = ?", workId).Find(&s)
	return c.JSON(http.StatusOK, commom.SuccessPayload(s))
}

func FetchBoard(c yee.Context) (err error) {
	var board model.CoreGlobalConfiguration
	model.DB().Select("board").First(&board)
	return c.JSON(http.StatusOK, commom.SuccessPayload(board))
}

func FetchTplAuditor(source string) []string {
	var tpl model.CoreWorkflowTpl
	var list []tpl2.Tpl
	model.DB().Model(model.CoreWorkflowTpl{}).Where("source =?", source).First(&tpl)
	_ = json.Unmarshal(tpl.Steps, &list)
	if len(list) > 1 {
		if len(list[1].Auditor) > 0 {
			return list[1].Auditor
		}
		return nil
	}
	return nil
}
