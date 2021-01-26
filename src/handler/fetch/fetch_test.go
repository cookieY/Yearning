package fetch

import (
	"Yearning-go/src/model"
	"runtime"
	"strings"
	"testing"
)

/*
 y 测试表共33.2W数据,模拟大量回滚语句提交的效率
*/

type Y struct {
	Test string `json:"test"`
}

func init() {
	model.DbInit("../../../conf.toml")
}

func BenchmarkConversionList2Str(b *testing.B) {
	var roll []Y
	model.DB().Table("y").Select("`test`").Find(&roll)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		var sql strings.Builder
		for i := range roll {
			sql.WriteString(roll[i].Test)
			sql.WriteString("\n")
		}
		_ = sql.String()
	}
}

func BenchmarkConversionList2Str2(b *testing.B) {
	var roll []Y
	model.DB().Table("y").Select("`test`").Find(&roll)
	b.ReportAllocs()
	b.SetBytes(1024 * 1024)
	for i := 0; i < b.N; i++ {
		var sql string
		for _, i := range roll {
			sql += i.Test + "\n"
		}
	}
}

func BenchmarkConversion(b *testing.B) {
	runtime.GOMAXPROCS(runtime.NumCPU())
	b.Run("BenchmarkConversionList2Str", BenchmarkConversionList2Str)
	b.Run("BenchmarkConversionList2Str2", BenchmarkConversionList2Str2)
}

/*
BenchmarkConversion/BenchmarkConversionList2Str
BenchmarkConversion/BenchmarkConversionList2Str-12         	      68	  15027904 ns/op	  69.78 MB/s	21982418 B/op	   92831 allocs/op
BenchmarkConversion/BenchmarkConversionList2Str2
BenchmarkConversion/BenchmarkConversionList2Str2-12        	       1	64609893380 ns/op	   0.02 MB/s	442996562712 B/op	 6644255 allocs/op
*/
