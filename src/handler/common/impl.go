package common

import (
	"Yearning-go/src/model"
	"gorm.io/gorm"
)

func (g *GeneralList[any]) ToMessage() Resp {
	return SuccessPayload(g)
}

func (p *PageList[any]) ToMessage() Resp {
	return SuccessPayload(map[string]interface{}{"data": p.Data, "page": p.Page})
}

func (p *PageList[any]) Paging() *PageList[any] {
	p.startAt = (p.Current * p.PageSize) - p.PageSize
	p.endAt = p.PageSize
	return p
}

func (p *PageList[any]) OrderBy(order string) *PageList[any] {
	p.Order = order
	return p
}

func (p *PageList[any]) Select(sel string) *PageList[any] {
	p.sel = sel
	return p
}

func (p *PageList[any]) Query(scopes ...func(*gorm.DB) *gorm.DB) *PageList[any] {
	if p.sel == "" {
		p.sel = "*"
	}
	if p.Order == "" {
		p.Order = "id desc"
	}
	model.DB().Select(p.sel).Model(p.Data).Scopes(scopes...).Count(&p.Page).Order(p.Order).
		Offset(p.startAt).Limit(p.endAt).Find(&p.Data)
	return p
}
