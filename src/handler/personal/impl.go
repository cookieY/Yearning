package personal

const (
	ORDER_POST_SUCCESS = "工单已提交,请等待审核人审核！"
	ER_DB_CONNENT = "数据库实例连接失败！请检查相关配置是否正确！"
	CUSTOM_INFO_SUCCESS = "邮箱/真实姓名修改成功！刷新后显示最新信息!"
	CUSTOM_PASSWORD_SUCCESS = "密码修改成功！"
)

type queryBind struct {
	Table string `json:"table"`
	DataBase string `json:"data_base"`
	Source string `json:"source"`
}