package model

type Other struct {
	Limit       uint64   `json:"limit"`
	IDC         []string `json:"idc"`
	Query       bool     `json:"query"`
	Register    bool     `json:"register"`
	Export      bool     `json:"export"`
	ExQueryTime int      `json:"ex_query_time"`
	Domain      string   `json:"domain"`
}

type Message struct {
	WebHook  string `json:"web_hook"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	ToUser   string `json:"to_user"`
	Mail     bool   `json:"mail"`
	Ding     bool   `json:"ding"`
	Ssl      bool   `json:"ssl"`
	PushType bool   `json:"push_type"`
	Key      string `json:"key"`
}

type Ldap struct {
	Url          string `json:"url"`
	User         string `json:"user"`
	Password     string `json:"password"`
	Type         string `json:"type"`
	Sc           string `json:"sc"`
	Ldaps        bool   `json:"ldaps"`
	Map          string `json:"map"`
	TestUser     string `json:"test_user"`
	TestPassword string `json:"test_password"`
}

type LabelWithValue struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type PermissionList struct {
	DDLSource   []string `json:"ddl_source"`
	DMLSource   []string `json:"dml_source"`
	QuerySource []string `json:"query_source"`
}

type Permission struct {
	Permissions PermissionList `json:"permissions"`
}
