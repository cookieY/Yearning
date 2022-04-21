package lib

import (
	"github.com/Jeffail/gabs/v2"
	"sync"
)

func ArrayRemove(source []byte, flag string) ([]byte, error) {
	p, err := gabs.ParseJSON(source)
	if err != nil {
		return nil, err
	}
	for i, c := range p.Children() {
		if c.Data().(string) == flag {
			_ = p.ArrayRemove(i)
		}
	}
	return p.EncodeJSON(), nil
}

func MultiArrayRemove(source []byte, sep []string, flag string) ([]byte, error) {
	p, err := gabs.ParseJSON(source)
	if err != nil {
		return nil, err
	}
	var wait sync.WaitGroup
	wait.Add(len(sep))
	for _, dl := range sep {
		go func(wait *sync.WaitGroup, dl string) {
			for i, c := range p.S(dl).Children() {
				if c.Data().(string) == flag {
					_ = p.ArrayRemove(i, dl)
				}
			}
			wait.Done()
		}(&wait, dl)
	}
	wait.Wait()
	return p.EncodeJSON(), nil
}
