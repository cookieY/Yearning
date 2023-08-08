package service

import (
	"Yearning-go/src/lib"
	"Yearning-go/src/model"
	"github.com/cookieY/yee/logger"
	"github.com/robfig/cron/v3"
	"time"
)

func cronTabMaskQuery() {
	crontab := cron.New()
	if _, err := crontab.AddFunc("* * * * *", func() {
		var queryOrder []model.CoreQueryOrder
		model.DB().Model(model.CoreQueryOrder{}).Where("`status` =?", 2).Find(&queryOrder)
		for _, i := range queryOrder {
			if lib.TimeDifference(i.ApprovalTime) {
				model.DB().Model(model.CoreQueryOrder{}).Where("work_id =?", i.WorkId).Updates(&model.CoreQueryOrder{Status: 3})
			}
		}
	}); err != nil {
		logger.DefaultLogger.Error(err)
	}
	crontab.Start()
}

func cronTabTotalTickets() {
	crontab := cron.New()
	if _, err := crontab.AddFunc("15 2 * * *", func() {
		var totalOrder int64
		var totalQuery int64
		model.DB().Model(model.CoreSqlOrder{}).Where("DATE(date) = CURDATE() - INTERVAL 1 DAY").Count(&totalOrder)
		model.DB().Model(model.CoreQueryOrder{}).Where("DATE(date) = CURDATE() - INTERVAL 1 DAY").Count(&totalQuery)
		model.DB().Model(model.CoreTotalTickets{}).Create(&model.CoreTotalTickets{TotalOrder: totalOrder, TotalQuery: totalQuery, Date: time.Now().Format("2006-01-02")})
	}); err != nil {
		logger.DefaultLogger.Error(err)
	}
	crontab.Start()
}
