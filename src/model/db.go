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
	"Yearning-go/src/i18n"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/cookieY/yee/logger"
	mmsql "github.com/go-sql-driver/mysql"
	drive "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"time"
)

var sqlDB *gorm.DB

type DSN struct {
	Username string
	Password string
	Host     string
	Port     int
	DBName   string
	CA       string
	Cert     string
	Key      string
}

func initConfig(cPath string) {
	_, err := toml.DecodeFile(cPath, &C)
	if err != nil {
		logger.DefaultLogger.Error(err)
	}
	JWT = os.Getenv("SECRET_KEY")
	i18n.MakeBuild(C.General.Lang)
	DefaultLogger = logger.LogCreator(int(TransferLogLevel()))
}

func DBNew(c string) {
	initConfig(c)
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_ADDR"), os.Getenv("MYSQL_DB"))
	if os.Getenv("MYSQL_USER") == "" {
		JWT = C.General.SecretKey
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", C.Mysql.User, C.Mysql.Password, C.Mysql.Host, C.Mysql.Port, C.Mysql.Db)
	}
	db, err := gorm.Open(drive.New(drive.Config{
		DSN:                       dsn,
		DefaultStringSize:         256,   // string 类型字段的默认长度
		SkipInitializeWithVersion: false, // 根据当前 MySQL 版本自动配置
	}), &gorm.Config{})
	if err != nil {
		logger.DefaultLogger.Error(i18n.DefaultLang.Load(i18n.ER_MYSQL_CONNECTION_FAILED))
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

func NewDBSub(dsn DSN) (*gorm.DB, error) {
	d, err := InitDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(drive.New(drive.Config{
		DSN:                       d,
		DefaultStringSize:         256,   // string 类型字段的默认长度
		SkipInitializeWithVersion: false, // 根据当前 MySQL 版本自动配置
	}), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return db, nil
}

func Close(db *gorm.DB) error {
	orm, err := db.DB()
	if err != nil {
		return err
	}
	return orm.Close()
}

func InitDSN(dsn DSN) (string, error) {
	isTLS := false
	if dsn.CA != "" && dsn.Cert != "" && dsn.Key != "" {
		isTLS = true
		certPool := x509.NewCertPool()
		if ok := certPool.AppendCertsFromPEM([]byte(dsn.CA)); !ok {
			return "", fmt.Errorf("failed to append ca certs")
		}
		clientCert := make([]tls.Certificate, 0, 1)
		certs, err := tls.X509KeyPair([]byte(dsn.Cert), []byte(dsn.Key))
		if err != nil {
			return "", err
		}
		clientCert = append(clientCert, certs)
		_ = mmsql.RegisterTLSConfig("custom", &tls.Config{
			RootCAs:            certPool,
			Certificates:       clientCert,
			InsecureSkipVerify: true,
		})
	}
	cfg := mmsql.Config{
		User:                 dsn.Username,
		Passwd:               dsn.Password,
		Addr:                 fmt.Sprintf("%s:%d", dsn.Host, dsn.Port), //IP:PORT
		Net:                  "tcp",
		DBName:               dsn.DBName,
		Loc:                  time.Local,
		AllowNativePasswords: true,
		ParseTime:            true,
	}
	if isTLS == true {
		cfg.TLSConfig = "custom"
	}
	return cfg.FormatDSN(), nil
}
