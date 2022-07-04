package model

import (
	"gorm.io/gorm"
	"sync"
	"testing"
)

func TestJsonGet(t *testing.T) {
	//var a GlobalConfiguration
	//var c Inception
	//JsonGet(&a)
	//if err := json.Unmarshal(a.Inception, &c); err != nil {
	//
	//}
	//fmt.Println(c.BackUser)
}

func Mock(db *gorm.DB, wg *sync.WaitGroup) {
	for i := 0; i < 100000; i++ {
		db.Create(&CoreDataSource{
			IDC:      "321",
			Username: "3211",
			Password: "cxnudchsj",
			Port:     2,
			Source:   "cnnskj",
			IP:       "156237123032810",
			IsQuery:  1,
		})
	}
	wg.Done()
}

func TestMockData(t *testing.T) {
	var wg sync.WaitGroup
	db2, _ := gorm.Open("mysql", "root:19931003@tcp(127.0.0.1:3306)/test01?charset=utf8")
	defer db2.Close()
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go Mock(db2, &wg)
	}
	wg.Wait()
}
