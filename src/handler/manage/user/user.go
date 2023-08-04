package user

import (
	"Yearning-go/src/handler/common"
	"Yearning-go/src/handler/manage/tpl"
	"Yearning-go/src/i18n"
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"encoding/json"
	"fmt"
	"github.com/cookieY/yee"
	"net/http"
	"strings"
)

func SuperFetchUser(c yee.Context) (err error) {
	u := new(common.PageList[[]model.CoreAccount])
	if err := c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	u.Paging().Select(CommonExpr).Query(
		common.AccordingToRealName(u.Expr.RealName),
		common.AccordingToMail(u.Expr.Email),
		common.AccordingToUsername(u.Expr.Username),
		common.AccordingToOrderDept(u.Expr.Dept),
	)
	return c.JSON(http.StatusOK, u.ToMessage())
}

func SuperDeleteUser(c yee.Context) (err error) {

	if new(lib.Token).JwtParse(c).Username != "admin" {
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.ADMIN_HAVE_DELETE_OTHER)))
	}

	user := c.QueryParam("user")
	if user == "admin" {
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.ADMIN_NOT_DELETE)))
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
		return c.JSON(http.StatusOK, common.ERR_COMMON_MESSAGE(fmt.Errorf(i18n.DefaultLang.Load(i18n.USER_CANNOT_DELETE), user, strings.Join(tips, ","))))
	}

	tx := model.DB().Begin()
	model.DB().Where("username =?", user).Delete(&model.CoreAccount{})
	model.DB().Where("username =?", user).Delete(&model.CoreGrained{})
	tx.Commit()

	return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(fmt.Sprintf(i18n.DefaultLang.Load(i18n.USER_DELETE_SUCCESS), user)))
}

func ManageUserCreateOrEdit(c yee.Context) (err error) {
	u := new(CommonUserPost)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	switch c.QueryParam("tp") {
	case "principal":
		var account []model.CoreAccount
		model.DB().Model(&model.CoreAccount{}).Find(&account)
		return c.JSON(http.StatusOK, common.SuccessPayload(account))
	case "edit":
		return c.JSON(http.StatusOK, SuperUserEdit(u))
	case "add":
		return c.JSON(http.StatusOK, SuperUserRegister(u))
	case "password":
		model.DB().Model(&model.CoreAccount{}).Where("username = ?", u.Username).Updates(
			model.CoreAccount{Password: lib.DjangoEncrypt(u.Password, string(lib.GetRandom()))})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(i18n.DefaultLang.Load(i18n.USER_EDIT_PASSWORD_SUCCESS)))
	case "policy":
		g, _ := json.Marshal(u.Group)
		model.DB().Model(model.CoreGrained{}).Scopes(common.AccordingToUsernameEqual(u.Username)).Updates(model.CoreGrained{Group: g})
		return c.JSON(http.StatusOK, common.SuccessPayLoadToMessage(fmt.Sprintf(i18n.DefaultLang.Load(i18n.USER_PROLICY_EDIT_SUCCESS), u.Username)))
	}
	return c.JSON(http.StatusOK, common.ERR_REQ_PASSWORD_FAKE)
}

func ManageUserFetch(c yee.Context) (err error) {
	u := new(CommonUserGet)
	if err = c.Bind(u); err != nil {
		return c.JSON(http.StatusOK, common.ERR_REQ_BIND)
	}
	switch u.Tp {
	case "depend":
		return c.JSON(http.StatusOK, DelUserDepend(u.User))
	case "group":
		var p []model.CoreRoleGroup
		var userP model.CoreGrained
		model.DB().Find(&p)
		model.DB().Where("username=?", u.User).First(&userP)
		return c.JSON(http.StatusOK, common.SuccessPayload(map[string]interface{}{"group": userP.Group, "list": p}))
	}
	return c.JSON(http.StatusOK, common.ERR_REQ_FAKE)
}
