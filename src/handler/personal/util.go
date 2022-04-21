package personal

import (
	"Yearning-go/src/handler/order/audit"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"log"
	"time"
)

func CallAutoTask(order *model.CoreSqlOrder, length int) {
	// todo 以下代码为autoTask代码
	var autoTask model.CoreAutoTask
	var source model.CoreDataSource
	if model.DB().Model(model.CoreAutoTask{}).
		Where("source_id = ? and data_base =? and `table` =?", order.SourceId, order.DataBase, order.Table).
		First(&autoTask).RecordNotFound() {
		return
	}
	var isCall bool
	model.DB().Model(model.CoreDataSource{}).Where("source_id =?", order.SourceId).First(&source)
	if client := lib.NewRpc(); client != nil {
		if err := client.Call("Engine.Exec", &audit.ExecArgs{
			Order:         order,
			Rules:         model.GloRole,
			IP:            source.IP,
			Port:          source.Port,
			Username:      source.Username,
			Password:      lib.Decrypt(source.Password),
			Message:       model.GloMessage,
			MaxAffectRows: autoTask.Affectrow,
		}, &isCall); err != nil {
			log.Println(err)
		}
	}
	if isCall {
		model.DB().Create(&model.CoreWorkflowDetail{
			WorkId:   order.WorkId,
			Username: "AutoTask Robot",
			Time:     time.Now().Format("2006-01-02 15:04"),
			Action:   audit.ORDER_EXECUTE_STATE,
		})
		model.DB().Model(model.CoreSqlOrder{}).Where("work_id =?", order.WorkId).Update(&model.CoreSqlOrder{CurrentStep: length})
	}

}
