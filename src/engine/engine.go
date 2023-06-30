package engine

type AuditRole struct {
	DMLAllowLimitSTMT              bool   `json:"DMLAllowLimitSTMT"` // 是否允许update/insert 语句使用limit关键字
	DMLInsertColumns               bool   `json:"DMLInsertColumns"`  //是否检查插入语句存在列名
	DMLMaxInsertRows               int    `json:"DMLMaxInsertRows"`  //inert语句最大多少个字段
	DMLWhere                       bool   `json:"DMLWhere"`          //是否检查dml语句where条件
	DMLAllowInsertNull             bool   // 允许insert语句插入Null值
	DMLOrder                       bool   // 是否检查dml语句order条件
	DMLSelect                      bool   //是否检查dml语句有select语句
	DMLInsertMustExplicitly        bool   //是否检查insert语句必须显式声明字段
	DDLEnablePrimaryKey            bool   // 是否检查必须拥有主键
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
	DDLMultiToCommit               bool   //是否允许一个工单内有多条DDL语句
	DDLPrimaryKeyMust              bool   //是否强制主键名为id
	DDLAllowColumnType             bool   // ddl语句允许更改字段类型
	DDLAllowMultiAlter             bool   // ddl语句允许多个alter语句
	DDLImplicitTypeConversion      bool
	DDLAllowPRINotInt              bool
	DDLEnableForeignKey            bool   // 允许外键
	DDLTablePrefix                 string // 指定表名前缀
	DDLColumnsMustHaveIndex        string // 如果表包含以下列，列必须有索引。可指定多个列,以逗号分隔.列类型可选.   格式: 列名 [列类型,可选],...
	DDLAllowChangeColumnPosition   bool   // ddl语句允许使用after/first
	DDLCheckFloatDouble            bool   //float/double 类型 强制变更为decimal类型
	IsOSC                          bool
	OSCExpr                        string
	OscSize                        uint
	AllowCreateView                bool
	AllowCrateViewWithSelectStar   bool
	AllowCreatePartition           bool
	AllowSpecialType               bool
	PRIRollBack                    bool
}

type Record struct {
	SQL              string   `json:"sql"`
	AffectRows       uint     `json:"affect_rows"`
	Status           string   `json:"status"`
	Error            string   `json:"error"`
	Level            uint8    `json:"level"`
	ExecTime         string   `json:"exec_time"`
	Table            string   `json:"table"`
	Schema           string   `json:"schema"`
	isOSC            bool     `json:"is_osc"`
	InsulateWordList []string `json:"insulate_word_list"`
}

type CheckArgs struct {
	SQL      string
	Schema   string
	Kind     int
	Lang     string
	Rule     AuditRole
	IP       string
	Username string
	Port     int
	Password string
	CA       string
	Cert     string
	Key      string
}
