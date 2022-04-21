package user

import (
	"Yearning-go/src/handler/commom"
	"Yearning-go/src/handler/manage/tpl"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"net/http"
	"strings"
)

func SuperFetchUser(c yee.Context) (err error) {
	u := new(commom.PageChange)
	if err := c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	var user []model.CoreAccount
	var count int
	start, end := lib.Paging(u.Current, u.PageSize)
	model.DB().Model(model.CoreAccount{}).Select(CommonExpr).Scopes(
		commom.AccordingToRealName(u.Expr.RealName),
		commom.AccordingToMail(u.Expr.Email),
		commom.AccordingToUsername(u.Expr.Username),
		commom.AccordingToOrderDept(u.Expr.Dept),
	).Count(&count).Offset(start).Limit(end).Find(&user)
	return c.JSON(http.StatusOK, commom.SuccessPayload(commom.CommonList{
		Page:  count,
		Data:  user,
		Multi: model.GloOther.Multi,
	}))
}

func SuperDeleteUser(c yee.Context) (err error) {

	if new(lib.Token).JwtParse(c).Username != "admin" {
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(ADMIN_HAVE_DELETE_OTHER))
	}

	user := c.QueryParam("user")
	if user == "admin" {
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(ADMIN_NOT_DELETE))
	}
	var flows []model.CoreWorkflowTpl
	var step []tpl.Tpl
	var tips []string
	model.DB().Find(&flows)
	for _, i := range flows {
		json.Unmarshal(i.Steps, &step)
		for _, j := range step {
			for _, k := range j.Auditor {
				if k == user {
					tips = append(tips, i.Source)
				}
			}
		}
	}
	if len(tips) != 0 {
		return c.JSON(http.StatusOK, commom.ERR_COMMON_MESSAGE(fmt.Errorf(USER_CANNOT_DELETE, user, strings.Join(tips, ","))))
	}

	tx := model.DB().Begin()
	model.DB().Where("username =?", user).Delete(&model.CoreAccount{})
	model.DB().Where("username =?", user).Delete(&model.CoreGrained{})
	tx.Commit()

	return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(fmt.Sprintf(USER_DELETE_SUCCESS, user)))
}

func ManageUserCreateOrEdit(c yee.Context) (err error) {
	u := new(CommonUserPost)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	switch c.QueryParam("tp") {
	case "edit":
		return c.JSON(http.StatusOK, SuperUserEdit(u))
	case "add":
		return c.JSON(http.StatusOK, SuperUserRegister(u))
	case "password":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", u.Username).Update(
			model.CoreAccount{Password: lib.DjangoEncrypt(u.Password, string(lib.GetRandom()))})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(USER_EDIT_PASSWORD_SUCCESS))
	case "policy":
		g, _ := json.Marshal(u.Group)
		model.DB().Model(model.CoreGrained{}).Scopes(commom.AccordingToUsernameEqual(u.Username)).Updates(model.CoreGrained{Group: g})
		return c.JSON(http.StatusOK, commom.SuccessPayLoadToMessage(fmt.Sprintf(USER_PROLICY_EDIT_SUCCESS, u.Username)))
	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_PASSWORD_FAKE)
}

func ManageUserFetch(c yee.Context) (err error) {
	u := new(CommonUserGet)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, commom.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "depend":
		return c.JSON(http.StatusOK, DelUserDepend(u.User))
	case "group":
		var p []model.CoreRoleGroup
		var userP model.CoreGrained
		model.DB().Find(&p)
		model.DB().Where("username=?", u.User).First(&userP)
		return c.JSON(http.StatusOK, commom.SuccessPayload(map[string]interface{}{"group": userP.Group, "list": p}))
	}
	return c.JSON(http.StatusOK, commom.ERR_REQ_FAKE)
}
