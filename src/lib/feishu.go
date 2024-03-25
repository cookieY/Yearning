package lib

import (
	"Yearning-go/src/model"
	"time"

	bot "github.com/crazykun/feishu-bot-markdown"
)

var TmplTestFeishu = &FeishuMsg{
	Title:    "Yearning 测试！",
	No:       "2024022800000000",
	Username: "pony",
	Assigned: "admin",
	Remark:   "测试",
	Status:   "<font color='green'>成功</font>",
	Link:     "http://127.0.0.1:8000/",
}

type FeishuMsg struct {
	Title    string          `json:"title"`            // 工单标题
	No       string          `json:"no"`               // 工单编号
	Db       string          `json:"db,omitempty"`     // 数据库
	Username string          `json:"username"`         // 提交人员
	Assigned string          `json:"assigned"`         // 审核人员
	Remark   string          `json:"remark,omitempty"` // 工单说明
	Link     string          `json:"link,omitempty"`   // 链接
	Status   string          `json:"status"`           // 状态
	Color    bot.FeishuColor `json:"-"`                // 卡片颜色
}

// 发送消息
func SendFeishuMsg(msg model.Message, f *FeishuMsg) error {
	var textMap = map[string]interface{}{
		"工单编号": f.No,
		"数据源":  f.Db,
		"提交人员": f.Username,
		"审核人员": f.Assigned,
		"工单说明": f.Remark,
		"状态":   f.Status,
		"时间":   time.Now().Format("2006-01-02 15:04:05"),
	}

	var textFeishu = &bot.FeishuMsg{
		Title:       f.Title,
		Markdown:    textMap,
		Link:        f.Link,
		HeaderColor: f.Color,
	}
	return bot.SendFeishuMsg(msg.WebHook, textFeishu)
}
