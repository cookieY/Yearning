package fetch

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
)

const (
	UNDO_EXPR            = "username =? AND work_id =? AND `status` =? "
	UNDO_MESSAGE_ERROR   = "工单状态已更改！无法撤销"
	UNDO_MESSAGE_SUCCESS = "工单已撤销！"
	AUDITOR_IS_NOT_EXIST = "流程信息缺失,请检查该数据源流程配置!"
	COMMENT_IS_POST      = "评论发送成功"
)

type referOrder struct {
	Data model.CoreSqlOrder `json:"data"`
	SQLs string             `json:"sqls"`
	Tp   int                `json:"tp"`
}

type PageSizeRef struct {
	WorkId   string `json:"work_id"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type _FetchBind struct {
	IDC      string             `json:"idc"`
	Tp       string             `json:"tp"`
	Source   string             `json:"source"`
	SourceId string             `json:"source_id"`
	DataBase string             `json:"data_base"`
	Table    string             `json:"table"`
	Rows     []common.FieldInfo `json:"rows"`
	Idx      []common.IndexInfo `json:"idx"`
	Hide     bool               `json:"hide"`
}

func (u *_FetchBind) FetchTableFieldsOrIndexes() error {
	var s model.CoreDataSource

	model.DB().Where("source_id =?", u.SourceId).First(&s)

	ps := lib.Decrypt(model.JWT, s.Password)
	db, err := model.NewDBSub(model.DSN{
		Username: s.Username,
		Password: ps,
		Host:     s.IP,
		Port:     s.Port,
		DBName:   u.DataBase,
		CA:       s.CAFile,
		Cert:     s.Cert,
		Key:      s.KeyFile,
	})
	if err != nil {
		return err
	}

	defer model.Close(db)

	if err := db.Raw(fmt.Sprintf("SHOW FULL FIELDS FROM `%s`.`%s`", u.DataBase, u.Table)).Scan(&u.Rows).Error; err != nil {
		return err
	}

	if err := db.Raw(fmt.Sprintf("SHOW INDEX FROM `%s`.`%s`", u.DataBase, u.Table)).Scan(&u.Idx).Error; err != nil {
		return err
	}
	return nil
}
