package db

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	drive "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	CONN_TEST_SUCCESS = "数据库实例连接成功！"
	DB_SAVE_SUCCESS   = "连接名添加成功！"
	ERR_DB_SAVE       = "config.toml文件中SecretKey值必须为16位！"
	DB_EDIT_SUCCESS   = "数据源信息已更新!"
)

type CommonDBPost struct {
	Encrypt       bool                 `json:"encrypt"`
	Tp            string               `json:"tp"`
	DB            model.CoreDataSource `json:"db"`
	ExcludeDbList []string             `json:"exclude_db_list"`
	WordList      []string             `json:"word_list"`
}

func ConnTest(u *model.CoreDataSource) error {
	db, err := gorm.Open(drive.New(drive.Config{
		DSN:                       fmt.Sprintf("%s:%s@(%s:%d)/?charset=utf8&parseTime=True&loc=Local", u.Username, u.Password, u.IP, u.Port),
		DefaultStringSize:         256,   // string 类型字段的默认长度
		SkipInitializeWithVersion: false, // 根据当前 MySQL 版本自动配置
	}), &gorm.Config{})
	if err != nil {
		return err
	}
	d, _ := db.DB()
	return d.Close()
}

func SuperEditSource(source *model.CoreDataSource) common.Resp {

	if source.Password != "" && lib.Decrypt(source.Password) == "" {
		model.DB().Model(&model.CoreDataSource{}).Where("source_id =?", source.SourceId).Updates(&model.CoreDataSource{Password: lib.Encrypt(source.Password)})
	}
	model.DB().Model(&model.CoreDataSource{}).Where("source_id =?", source.SourceId).Updates(map[string]interface{}{
		"id_c":               source.IDC,
		"ip":                 source.IP,
		"port":               source.Port,
		"source":             source.Source,
		"username":           source.Username,
		"is_query":           source.IsQuery,
		"flow_id":            source.FlowID,
		"exclude_db_list":    source.ExcludeDbList,
		"principal":          source.Principal,
		"insulate_word_list": source.InsulateWordList,
	})
	var k []model.CoreRoleGroup
	model.DB().Find(&k)
	for i := range k {
		var p model.PermissionList
		if err := json.Unmarshal(k[i].Permissions, &p); err != nil {
			return common.ERR_COMMON_MESSAGE(err)
		}
		if source.IsQuery == 0 {
			p.QuerySource = lib.ResearchDel(p.QuerySource, source.Source)
		}
		if source.IsQuery == 1 {
			p.DDLSource = lib.ResearchDel(p.DDLSource, source.Source)
			p.DMLSource = lib.ResearchDel(p.DMLSource, source.Source)
		}
		r, _ := json.Marshal(p)
		model.DB().Model(&model.CoreRoleGroup{}).Where("id =?", k[i].ID).Updates(model.CoreRoleGroup{Permissions: r})
	}
	return common.SuccessPayLoadToMessage(DB_EDIT_SUCCESS)
}

func SuperCreateSource(source *model.CoreDataSource) common.Resp {
	source.Password = lib.Encrypt(source.Password)
	if source.Password != "" {
		source.SourceId = uuid.New().String()
		model.DB().Create(source)
		return common.SuccessPayLoadToMessage(DB_SAVE_SUCCESS)
	}
	return common.SuccessPayLoadToMessage(ERR_DB_SAVE)
}

func SuperTestDBConnect(source *model.CoreDataSource) common.Resp {
	if err := ConnTest(source); err != nil {
		return common.ERR_COMMON_MESSAGE(err)
	}
	return common.SuccessPayLoadToMessage(CONN_TEST_SUCCESS)
}
