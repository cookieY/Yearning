package commom

type ExecuteStr struct {
	WorkId  string `json:"work_id"`
	Perform string `json:"perform"`
	Page    int    `json:"page"`
	Flag    int    `json:"flag"`
	Text    string `json:"text"`
	Tp      string `json:"tp"`
}

type PageInfo struct {
	Page int    `json:"page"`
	Find Search `json:"find"`
	Tp   string `json:"tp"`
}

type CommonList struct {
	Page    int         `json:"page"`
	Data    interface{} `json:"data"`
	IDC     []string    `json:"idc"`
	Source  interface{} `json:"source"`
	Query   interface{} `json:"query"`
	Auditor interface{} `json:"auditor"`
	Multi   bool        `json:"multi"`
}

type Search struct {
	Picker   []string `json:"picker"`
	Valve    bool     `json:"valve"`
	Text     string   `json:"text"`
	Explain  string   `json:"explain"`
	WorkId   string   `json:"work_id"`
	Type     int      `json:"type"`
	Status   int      `json:"status"`
	IDC      string   `json:"idc"`
	Source   string   `json:"source"`
	Username string   `json:"username"`
	Dept     string   `json:"dept"`
}

type SQLTest struct {
	Source   string `json:"source"`
	SQL      string `json:"sql"`
	Database string `json:"data_base"`
	IsDML    bool   `json:"is_dml"`
	WorkId   string `json:"work_id"`
}

type QueryOrder struct {
	IDC      string `json:"idc"`
	Source   string `json:"source"`
	Export   uint   `json:"export"`
	Assigned string `json:"assigned"`
	Text     string `json:"text"`
	WorkId   string `json:"work_id"`
	Tp       string `json:"tp"`
}

const (
	ORDER_IS_CREATE = "工单已创建!"
	ORDER_IS_DUP = "工单请勿重复提交!"
	ORDER_IS_EDIT   = "工单已编辑！"
	ORDER_IS_DELETE = "工单已删除！"
	ORDER_IS_CLEAR = "工单已清除"
	ORDER_IS_AGREE = "工单已同意"
	ORDER_IS_REJECT = "工单已拒绝"
	ORDER_IS_ALL_END = "所有工单已终止"
	ORDER_IS_END = "工单已终止"
	ORDER_IS_ALL_CANCEL = "所有工单已取消"
	DATA_IS_DELETE  = "数据已删除！"
	DATA_IS_EDIT    = "数据已编辑！"
	DATA_IS_UPDATED = "数据已更新"
)
