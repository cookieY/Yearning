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

package parser

var FetchAuditRole AuditRole

type Record struct {
	SQL        string
	AffectRows int32
	Status     string
	Error      string
	Level      int32
}

type AuditRole struct {
	DMLAllowLimitSTMT              bool   `json:"DMLAllowLimitSTMT"` // 是否允许update/insert 语句使用limit关键字
	DMLInsertColumns               bool   `json:"DMLInsertColumns"`  //是否检查插入语句存在列名
	DMLMaxInsertRows               int    `json:"DMLMaxInsertRows"`  //inert语句最大多少个字段
	DMLWhere                       bool   `json:"DMLWhere"`          //是否检查dml语句where条件
	DMLOrder                       bool   // 是否检查dml语句order条件
	DMLSelect                      bool   //是否检查dml语句有select语句
	DDLCheckTableComment           bool   //是否检查表注释
	DDlCheckColumnComment          bool   //是否检查列注释
	DDLCheckColumnNullable         bool   //是否检查ddl语句有null值
	DDLCheckColumnDefault          bool   //是否检查列默认值
	DDLEnableAcrossDBRename        bool   //是否允许跨库表迁移
	DDLEnableAutoincrementInit     bool   //是否强制自增列初始值为1
	DDLEnableAutoIncrement         bool   //是否强制主键为自增列
	DDLEnableAutoincrementUnsigned bool   //是否检查自增列设置无符号标志unsigned
	DDLEnableDropTable             bool   //是否允许删除表
	DDLEnableDropDatabase          bool   //是否允许drop db
	DDLEnableNullIndexName         bool   //是否允许索引名为空
	DDLIndexNameSpec               bool   //是否开启索引名称规范  如开启 索引前缀必须以uniq/idx命名
	DDLMaxKeyParts                 uint   // 单个索引最多可以指定多少个字段
	DDLMaxKey                      uint   //单表最多可以指定索引数
	DDLMaxCharLength               uint   //char字段最大长度
	MaxTableNameLen                int    //表名最大长度
	MaxAffectRows                  uint   //最大影响行数
	MaxDDLAffectRows               uint   //最大影响行数
	SupportCharset                 string //允许的字符集范围
	SupportCollation               string //允许的排列顺序范围
	CheckIdentifier                bool   //是否检查关键词
	MustHaveColumns                string // 建表时必须拥有哪些字段以逗号分隔，值为空时不限制！
	DDLMultiToSubmit               bool   //是否允许一个工单内有多条DDL语句
	DDLPrimaryKeyMust              bool   //是否强制主键名为id
	DDLAllowColumnType             bool   // ddl语句允许更改字段类型
	DDLImplicitTypeConversion      bool
	DDLAllowPRINotInt              bool
	DDLColumnsMustHaveIndex        string // 如果表包含以下列，列必须有索引。可指定多个列,以逗号分隔.列类型可选.   格式: 列名 [列类型,可选],...
	DDLAllowChangeColumnPosition   bool   // ddl语句允许使用after/first
	DDLCheckFloatDouble            bool   //float/double 类型 强制变更为decimal类型
	IsOSC                          bool
	OscBinDir                      string // pt-osc path
	OscDropNewTable                bool
	OscDropOldTable                bool
	OscCheckReplicationFilters     bool
	OscCheckAlter                  bool
	OscAlterForeignKeysMethod      string
	OscMaxLag                      int
	OscRecursionMethod             string
	OscCheckInterval               int
	OscMaxThreadConnected          int
	OscMaxThreadRunning            int
	OscCriticalThreadConnected     int
	OscCriticalThreadRunning       int
	OscPrintSql                    bool
	OscChunkTime                   float32
	OscChunkSize                   float32
	OscSize                        uint
	AllowCreateView                bool
	AllowCreatePartition           bool
	AllowSpecialType               bool
	OscSleep                       float32
	OscCheckUniqueKeyChange        bool
	OscLockWaitTimeout             int
}

type IndexInfo struct {
	Table      string `gorm:"Column:Table"`
	NonUnique  int    `gorm:"Column:Non_unique"`
	IndexName  string `gorm:"Column:Key_name"`
	Seq        int    `gorm:"Column:Seq_in_index"`
	ColumnName string `gorm:"Column:Column_name"`
	IndexType  string `gorm:"Column:Index_type"`
}

type FieldInfo struct {
	Field      string  `gorm:"Column:Field" json:"field"`
	Type       string  `gorm:"Column:Type" json:"type"`
	Collation  string  `gorm:"Column:Collation" json:"collation"`
	Null       string  `gorm:"Column:Null" json:"null"`
	Key        string  `gorm:"Column:Key" json:"key"`
	Default    *string `gorm:"Column:Default" json:"default"`
	Extra      string  `gorm:"Column:Extra" json:"extra"`
	Privileges string  `gorm:"Column:Privileges" json:"privileges"`
	Comment    string  `gorm:"Column:Comment" json:"comment"`
}
