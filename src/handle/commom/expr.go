package commom

import (
	"github.com/jinzhu/gorm"
)

const QueryField = "work_id, username, text, backup, date, real_name, executor, `status`, `type`, `delay`, `source`,`id_c`,`data_base`,`table`,`execute_time`,assigned,current_step,relevant"

func AccordingToWorkId(workId string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("work_id like ?", "%"+workId+"%")
	}
}

func AccordingToQueryPer() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`query_per` in (?)", []int{1, 3})
	}
}

func AccordingToOrderState() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`status` in (?)", []int{1, 4})
	}
}

func AccordingToAssigned(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("`assigned` = ?", user)
	}
}

func AccordingToUsername(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("username like ?", "%"+user+"%")
	}
}

func AccordingToDatetime(time []string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("date >= ? AND date <= ?", time[0], time[1])
	}
}

func AccordingToRelevant(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {

		return db.Where("JSON_SEARCH(relevant, 'one', ?) IS NOT NULL", user)
	}
}

func AccordingToGuest(user string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("username = ?", user)
	}
}

func AccordingToText(text string) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("text like ?", "%"+text+"%")
	}
}
