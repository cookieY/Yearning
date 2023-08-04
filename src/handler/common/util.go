package common

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"errors"
	"fmt"
	"github.com/cookieY/yee/logger"
	"strings"
)

func unifiedLabel(col []string) []any {
	var s []any
	for i := 0; i < len(col); i++ {
		var t string
		s = append(s, &t)
	}
	return s
}

func ScanDataRows(s model.CoreDataSource, database, sql, meta string, isQuery bool, isLeaf bool) (*_dbInfo, error) {
	res := new(_dbInfo)
	ps := lib.Decrypt(model.JWT, s.Password)
	if ps == "" {
		return res, errors.New("连接失败,密码解析错误！")
	}

	db, err := model.NewDBSub(model.DSN{
		Username: s.Username,
		Password: ps,
		Host:     s.IP,
		Port:     s.Port,
		DBName:   database,
		CA:       s.CAFile,
		Cert:     s.Cert,
		Key:      s.KeyFile,
	})
	if err != nil {
		return res, err
	}

	defer func() {
		_ = model.Close(db)
	}()
	rows, err := db.Raw(sql).Rows()
	if err != nil {
		return nil, err
	}
	col, _ := rows.Columns()
	_tmp := unifiedLabel(col)
	if len(_tmp) == 0 {
		return nil, errors.New("field is empty")
	}
	excludeDbList := lib.MapOn(strings.Split(s.ExcludeDbList, ","))
	for rows.Next() {
		if err = rows.Scan(_tmp...); err != nil {
			logger.DefaultLogger.Error(err)
		}
		j := *_tmp[0].(*string)
		if isQuery {
			if len(excludeDbList) > 0 {
				if _, ok := excludeDbList[j]; ok {
					continue
				}
			}
			res.QueryList = append(res.QueryList, map[string]interface{}{"title": j, "key": checkMeta(j, database, meta), "meta": meta, "isLeaf": isLeaf})
		} else {
			res.Results = append(res.Results, j)
		}
	}
	return res, nil
}

func checkMeta(s, database, flag string) string {
	if flag == "Table" {
		return fmt.Sprintf("`%s`.`%s`", database, s)
	}
	return s
}

func Highlight(s *model.CoreDataSource, isField string, dbName string) []map[string]string {
	ps := lib.Decrypt(model.JWT, s.Password)
	var list []map[string]string
	db, err := model.NewDBSub(model.DSN{
		Username: s.Username,
		Password: ps,
		Host:     s.IP,
		Port:     s.Port,
		DBName:   "",
		CA:       s.CAFile,
		Cert:     s.Cert,
		Key:      s.KeyFile,
	})
	if err != nil {
		logger.DefaultLogger.Error(err)
		return nil
	}

	defer func() {
		_ = model.Close(db)
	}()

	var highlight string

	excludeDbList := strings.Split(s.ExcludeDbList, ",")

	if isField == "true" {
		tbl, err := db.Table("information_schema.tables").Select("table_name").Scopes(AccordingToSchemaIn(dbName)).Group("table_name").Rows()
		if err != nil {
			model.DefaultLogger.Debugf("fetch table error: %v", err)
		}
		for tbl.Next() {
			tbl.Scan(&highlight)
			list = append(list, map[string]string{"vl": highlight, "meta": "Table"})
		}
		fields, err := db.Table("information_schema.Columns").Select("COLUMN_NAME").Scopes(AccordingToSchemaIn(dbName)).Group("COLUMN_NAME").Rows()
		if err != nil {
			model.DefaultLogger.Debugf("fetch fields error: %v", err)
		}
		for fields.Next() {
			fields.Scan(&highlight)
			list = append(list, map[string]string{"vl": highlight, "meta": "Fields"})
		}
	} else {
		schema, err := db.Table("information_schema.SCHEMATA").Select("SCHEMA_NAME").Scopes(AccordingToSchemaNotIn(true, excludeDbList)).Group("SCHEMA_NAME").Rows()
		if err != nil {
			model.DefaultLogger.Debugf("fetch schema error: %v", err)
		}
		for schema.Next() {
			schema.Scan(&highlight)
			list = append(list, map[string]string{"vl": highlight, "meta": "Schema"})
		}
	}

	return list
}
func SuccessPayload(payload interface{}) Resp {
	return Resp{
		Payload: payload,
		Code:    1200,
	}
}

func SuccessPayLoadToMessage(text string) Resp {
	return Resp{
		Text: text,
		Code: 1200,
	}
}
