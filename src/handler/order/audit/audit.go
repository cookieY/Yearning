package audit

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	pb "Yearning-go/src/proto"
	"github.com/cookieY/yee"
	"net/http"
	"time"
)

func SuperSQLTest(c yee.Context) (err error) {
	u := new(commom.SQLTest)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var s model.CoreDataSource
	var order model.CoreSqlOrder
	model.DB().Where("work_id =?", u.WorkId).First(&order)
	model.DB().Where("source =?", order.Source).First(&s)
	y := pb.LibraAuditOrder{
		IsDML:    order.Type == 1,
		SQL:      order.SQL,
		DataBase: order.DataBase,
		Source: &pb.Source{
			Addr:     s.IP,
			User:     s.Username,
			Port:     int32(s.Port),
			Password: lib.Decrypt(s.Password),
		},
		Execute: false,
		Check:   true,
	}
	record, err := lib.TsClient(&y)
	if err != nil {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(err))
	}
	return c.JSON(http.StatusOK, commom.SuccessPayload(record))
}

func ExecuteOrder(c yee.Context) (err error) {
	u := new(commom.ExecuteStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)
	var order model.CoreSqlOrder
	model.DB().Where("work_id =?", u.WorkId).First(&order)

	var flowTpl model.CoreWorkflowTpl
	model.DB().Where("source =?", order.IDC).First(&flowTpl)

	var steps []tpl.Tpl
	_ = json.Unmarshal(flowTpl.Steps, &steps)

	var authCheck bool

	for _, v := range steps[order.CurrentStep].Auditor {
		if v == user {
			authCheck = true
			break
		}
	}

	if !authCheck {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(fmt.Errorf("你没有权限进行操作")))
	}

	if order.Status != 2 && order.Status != 5 {
		c.Logger().Error(IDEMPOTENT)
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(IDEMPOTENT))
	}

	if order.Type == 3 {
		model.DB().Model(&model.CoreSqlOrder{}).Where("work_id =?", u.WorkId).Updates(map[string]interface{}{"status": 1, "execute_time": time.Now().Format("2006-01-02 15:04"), "current_step": order.CurrentStep + 1})
		lib.MessagePush(u.WorkId, 1, "")
	} else {
		executor := new(Review)

		order.Assigned = user

		executor.Init(order).Executor()
	}
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.WorkId,
		Username: user,
		Rejected: "",
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   ORDER_EXECUTE_STATE,
	})

	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(ORDER_EXECUTE_STATE))
}

func AuditOrderState(c yee.Context) (err error) {
	u := new(commom.ExecuteStr)
	user, _ := lib.JwtParse(c)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}

	// add user check
	var order model.CoreSqlOrder
	model.DB().Where("work_id =?", u.WorkId).First(&order)

	var flowTpl model.CoreWorkflowTpl
	model.DB().Where("source =?", order.IDC).First(&flowTpl)

	var steps []tpl.Tpl
	_ = json.Unmarshal(flowTpl.Steps, &steps)

	if user == order.Username {
		return commom.ERR_COMMON_MESSAGE(fmt.Errorf("你是提交人不能进行操作"))
	}

	var authCheck bool

	for _, v := range steps[order.CurrentStep].Auditor {
		if v == user {
			authCheck = true
			break
		}
	}

	if !authCheck {
		return commom.ERR_COMMON_MESSAGE(fmt.Errorf(ORDER_AUTH_CHECK_ERR))
	}

	switch u.Tp {
	case "agree":
		return c.JSON(http.StatusOK, MultiAuditOrder(u, user))
	case "reject":
		return c.JSON(http.StatusOK, RejectOrder(u, user))
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}

//DelayKill will stop delay order
func DelayKill(c yee.Context) (err error) {
	u := new(commom.ExecuteStr)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	user, _ := lib.JwtParse(c)
	model.DB().Create(&model.CoreWorkflowDetail{
		WorkId:   u.WorkId,
		Username: user,
		Time:     time.Now().Format("2006-01-02 15:04"),
		Action:   ORDER_KILL_STATE,
	})
	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(delayKill(u.WorkId)))
}

func FetchAuditOrder(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	user, _ := lib.JwtParse(c)
	order := u.GetSQLOrderList(commom.AccordingToAllOrderState(u.Find.Status),
		commom.AccordingToRelevant(user),
		commom.AccordingToText(u.Find.Text),
		commom.AccordingToDatetime(u.Find.Picker))
	return c.JSON(http.StatusOK, commom.SuccessPayload(order))
}

func FetchRecord(c yee.Context) (err error) {
	u := new(commom.PageInfo)
	if err = c.Bind(u); err != nil {
		c.Logger().Error(err.Error())
		return
	}
	order := u.GetSQLOrderList(commom.AccordingToOrderState(),
		commom.AccordingToWorkId(u.Find.Text),
		commom.AccordingToDatetime(u.Find.Picker))
	return c.JSON(http.StatusOK, commom.SuccessPayload(order))
}

func AuditOrderApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "test":
		return SuperSQLTest(c)
	case "state":
		return AuditOrderState(c)
	case "execute":
		return ExecuteOrder(c)
	case "kill":
		return DelayKill(c)
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}

func AuditOrRecordOrderFetchApis(c yee.Context) (err error) {
	switch c.Params("tp") {
	case "list":
		return FetchAuditOrder(c)
	case "record":
		return FetchRecord(c)
	default:
		return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
	}
}
