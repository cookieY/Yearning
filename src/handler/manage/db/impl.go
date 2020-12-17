package db

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"fmt"
	"github.com/jinzhu/gorm"
)

const (
	ERR_CONN_TEST     = "数据库实例连接失败！请检查相关配置是否正确！"
	CONN_TEST_SUCCESS = "数据库实例连接成功！"
	DB_SAVE_SUCCESS   = "连接名添加成功！"
	ERR_DB_SAVE       = "config.toml文件中SecretKey值必须为16位！"
	ERR_DB_SAVE_DUP   = "连接名称重复,请更改为其他!"
	DB_EDIT_SUCCESS   = "数据源信息已更新!"
)

type fetchDB struct {
	Page int           ` json:"page"`
	Find commom.Search `json:"find"`
}

type CommonDBPost struct {
	Tp string               `json:"tp"`
	DB model.CoreDataSource `json:"db"`
}

func ConnTest(u *model.CoreDataSource) error {
	db, err := gorm.Open("mysql", fmt.Sprintf("%s:%s@(%s:%d)/?charset=utf8&parseTime=True&loc=Local", u.Username, u.Password, u.IP, u.Port))
	defer func() {
		_ = db.Close()
	}()
	if err != nil {
		return err
	}
	return nil
}

func SuperEditSource(source *model.CoreDataSource) commom.Resp {
	if source.Password == "***********" {
		model.DB().Model(&model.CoreDataSource{}).Where("source =?", source.Source).Updates(&model.CoreDataSource{IP: source.IP, Port: source.Port, Username: source.Username})
	} else {
		source.Password = lib.Encrypt(source.Password)
		model.DB().Model(&model.CoreDataSource{}).Where("source =?", source.Source).Updates(source)
	}

	return commom.SuccessPayLoadToMessage(DB_EDIT_SUCCESS)
}

func SuperCreateSource(source *model.CoreDataSource) commom.Resp {
	var refer model.CoreDataSource
	if model.DB().Where("source =?", source.Source).First(&refer).RecordNotFound() {
		source.Password = lib.Encrypt(source.Password)
		if source.Password != "" {
			model.DB().Create(source)
			return commom.SuccessPayLoadToMessage(DB_SAVE_SUCCESS)
		}
		return commom.SuccessPayLoadToMessage(ERR_DB_SAVE)
	}
	return commom.SuccessPayLoadToMessage(ERR_DB_SAVE_DUP)
}

func SuperTestDBConnect(source *model.CoreDataSource) commom.Resp {
	if err := ConnTest(source); err != nil {
		return commom.ERR_COMMON_MESSAGE(err)
	}

	return commom.SuccessPayLoadToMessage(CONN_TEST_SUCCESS)
}
