package lib

import (
	"Yearning-go/src/model"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func SendWechatMsg(msg model.Message, sv string) {
	//请求地址模板

	//创建一个请求

	var mx string

	hook := msg.WebHook

	mx = strings.Replace(sv, `\n \n`, `\n`, -1)
	mx = strings.Replace(mx, `"text":`, `"content":`, 1)
	if msg.Key != "" {
		hook = SignWechat(msg.Key, msg.WebHook)
	}

	req, err := http.NewRequest("POST", hook, strings.NewReader(mx))
	if err != nil {
		log.Println(err.Error())
		return
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := &http.Client{Transport: tr}
	//设置请求头
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	//发送请求
	resp, err := client.Do(req)
	if err != nil {
		log.Println(err.Error())
		return
	}

	//关闭请求
	resp.Body.Close()
}

func SignWechat(secret, hook string) string {
	timestamp := time.Now().UnixNano() / 1e6
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	sign := hmacSha256(stringToSign, secret)
	url := fmt.Sprintf("%s&timestamp=%d&sign=%s", hook, timestamp, sign)
	return url
}
