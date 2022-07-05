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

package model

import (
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/cookieY/yee/logger"
	drive "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"time"
)

var sqlDB *gorm.DB

func DBNew(c string) {
	_, err := toml.DecodeFile(c, &C)
	if err != nil {
		logger.DefaultLogger.Error(err)
	}
	JWT = C.General.SecretKey

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_ADDR"), os.Getenv("MYSQL_DB"))
	if os.Getenv("MYSQL_USER") == "" {
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", C.Mysql.User, C.Mysql.Password, C.Mysql.Host, C.Mysql.Port, C.Mysql.Db)
	}
	db, err := gorm.Open(drive.New(drive.Config{
		DSN:                       dsn,
		DefaultStringSize:         256,   // string 类型字段的默认长度
		SkipInitializeWithVersion: false, // 根据当前 MySQL 版本自动配置
	}), &gorm.Config{})
	if err != nil {
		logger.DefaultLogger.Error("mysql连接失败! 请检查配置信息")
		os.Exit(1)
		return
	}
	sqlDB = db
	conf, err := db.DB()
	if err != nil {
		logger.DefaultLogger.Error(err)
		return
	}
	conf.SetConnMaxLifetime(time.Minute * 10)
	conf.SetMaxOpenConns(50)
	conf.SetMaxIdleConns(15)
}

func DB() *gorm.DB {
	return sqlDB
}

func NewDBSub(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(drive.New(drive.Config{
		DSN:                       dsn,
		DefaultStringSize:         256,   // string 类型字段的默认长度
		SkipInitializeWithVersion: false, // 根据当前 MySQL 版本自动配置
	}), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return db, nil
}

func Close(db *gorm.DB) error {
	orm, _ := db.DB()
	return orm.Close()
}
