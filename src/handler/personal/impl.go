package personal

import (
	"Yearning-go/src/engine"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/cookieY/sqlx"
)

const (
	ORDER_POST_SUCCESS      = "工单已提交,请等待审核人审核！"
	ER_DB_CONNENT           = "数据库实例连接失败！请检查相关配置是否正确！"
	CUSTOM_INFO_SUCCESS     = "邮箱/真实姓名修改成功！刷新后显示最新信息!"
	CUSTOM_PASSWORD_SUCCESS = "密码修改成功！"
	ER_SQL_EMPTY            = "无查询语句！"
	BUF                     = 1<<20 - 1
	ER_RPC                  = "rpc调用失败"
)

type queryBind struct {
	Table    string `json:"table"`
	DataBase string `json:"data_base"`
	Source   string `json:"source"`
}

type QueryDeal struct {
	Ref struct {
		Type     int    `msgpack:"type"` //0 conn 1 close
		Sql      string `msgpack:"sql"`
		Schema   string `msgpack:"schema"`
		SourceId string `msgpack:"source_id"`
	}
	MultiSQLRunner []MultiSQLRunner
}

type MultiSQLRunner struct {
	SQL              string
	InsulateWordList map[string]struct{}
}

type Query struct {
	Field []map[string]interface{} `msgpack:"field"`
	Data  []map[string]interface{} `msgpack:"data"`
}

type QueryArgs struct {
	SQL              string
	Limit            uint64
	InsulateWordList string
}

func (q *QueryDeal) PreCheck(insulateWordList string) error {
	var rs []engine.Record
	if client := lib.NewRpc(); client != nil {
		if err := client.Call("Engine.Query", &QueryArgs{
			SQL:              q.Ref.Sql,
			Limit:            model.GloOther.Limit,
			InsulateWordList: insulateWordList,
		}, &rs); err != nil {
			return err
		}
		for _, i := range rs {
			if i.Error != "" {
				return errors.New(i.Error)
			}
			q.MultiSQLRunner = append(q.MultiSQLRunner, MultiSQLRunner{SQL: i.SQL, InsulateWordList: lib.MapOn(i.InsulateWordList)})
		}
		return nil
	}
	return errors.New(ER_RPC)
}

func (m *MultiSQLRunner) Run(db *sqlx.DB, schema string) (*Query, error) {
	query := new(Query)
	if db == nil {
		return nil, errors.New("数据库连接失败！")
	}
	db.Exec(fmt.Sprintf("use %s", schema))
	rows, err := db.Queryx(m.SQL)

	if err != nil {
		return nil, err
	}

	cols, err := rows.Columns()

	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		results := make(map[string]interface{})
		_ = rows.MapScan(results)
		for key := range results {
			switch r := results[key].(type) {
			case []uint8:
				if len(r) > BUF {
					results[key] = "blob字段无法显示"
				} else {
					switch hex.EncodeToString(r) {
					case "01":
						results[key] = "true"
					case "00":
						results[key] = "false"
					default:
						results[key] = string(r)
					}
					if m.excludeFieldContext(key) {
						results[key] = "****脱敏字段"
					}
				}
			}
		}
		query.Data = append(query.Data, results)
	}

	ele := removeDuplicateElement(cols)

	for cv := range ele {
		query.Field = append(query.Field, map[string]interface{}{"title": ele[cv], "dataIndex": ele[cv], "width": 200, "resizable": true, "ellipsis": true})
	}
	query.Field[0]["fixed"] = "left"

	return query, nil
}

func (m *MultiSQLRunner) excludeFieldContext(field string) bool {
	_, ok := m.InsulateWordList[field]
	return ok
}

func removeDuplicateElement(addrs []string) []string {
	result := make([]string, 0, len(addrs))
	temp := map[string]struct{}{}
	idx := 0
	for _, item := range addrs {
		if _, ok := temp[item]; !ok {
			temp[item] = struct{}{}
			result = append(result, item)
		} else {
			idx++
			item += fmt.Sprintf("(%v)", idx)
			result = append(result, item)
		}
	}
	return result
}
