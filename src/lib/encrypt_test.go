package lib

import (
	"fmt"
	"testing"
)

func TestEncryptToken_Encrypt(t *testing.T) {
	//ts := EncryptToken{
	//	Password: "Yearning_admin",
	//	Key: "ZAQdOtbgynh2",
	//}
	//e := Encrypt()
	//fmt.Println(e)
	d := Decrypt("XTwgVLwyFSR3Rc6IjYaDcg==")
	fmt.Println(d)
	//c := DjangoEncrypt("Yearning_admin", "321312312321")
	//fmt.Println(c)
	//perfix :=strings.Split(c,"$")[2]
	//fmt.Println(perfix)
}
