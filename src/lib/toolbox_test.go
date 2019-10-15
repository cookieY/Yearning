package lib

import (
	"fmt"
	"testing"
)

func TestIntersect(t *testing.T) {
	a := []string{"a","c","v"}
	b :=  []string{"a","cfniuwdfhiuw","z","326183681","c"}
	fmt.Println(Intersect(a,b))
}

func TestTimeDifference(t *testing.T)  {
	a  := TimeDifference("2019-08-01 16:59")
	fmt.Println(a)
}
