package db

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"github.com/google/uuid"
	drive "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type CommonDBPost struct {
	Encrypt       bool                 `json:"encrypt"`
	Tp            string               `json:"tp"`
	DB            model.CoreDataSource `json:"db"`
	ExcludeDbList []string             `json:"exclude_db_list"`
	WordList      []string             `json:"word_list"`
}

func ConnTest(u *model.CoreDataSource) error {
	dsn, err := model.InitDSN(model.DSN{
		Username: u.Username,
		Password: u.Password,
		Host:     u.IP,
		Port:     u.Port,
		DBName:   "",
		CA:       u.CAFile,
		Cert:     u.Cert,
		Key:      u.KeyFile,
	})
	if err != nil {
		return err
	}
	db, err := gorm.Open(drive.New(drive.Config{
		DSN:                       dsn,
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

	if source.Password != "" && lib.Decrypt(model.JWT, source.Password) == "" {
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
		"ca_file":            source.CAFile,
		"cert":               source.Cert,
		"key_file":           source.KeyFile,
		"rule_id":            source.RuleId,
	})
	model.DB().Model(model.CoreQueryOrder{}).Where("status =? and source_id =?", 1, source.SourceId).Updates(&model.CoreQueryOrder{Assigned: source.Principal})
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
	return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.DB_EDIT_SUCCESS))
}

func SuperCreateSource(source *model.CoreDataSource) common.Resp {
	source.Password = lib.Encrypt(source.Password)
	if source.Password != "" {
		source.SourceId = uuid.New().String()
		model.DB().Create(source)
		return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.DB_SAVE_SUCCESS))
	}
	return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.ERR_DB_SAVE))
}

func SuperTestDBConnect(source *model.CoreDataSource) common.Resp {
	if err := ConnTest(source); err != nil {
		return common.ERR_COMMON_MESSAGE(err)
	}
	return common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.CONN_TEST_SUCCESS))
}
