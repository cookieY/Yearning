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

func SendDingMsg(msg model.Message, sv string) {
	//请求地址模板

	//创建一个请求

	var mx string

	hook := msg.WebHook

	mx = fmt.Sprintf(`{"msgtype": "markdown", "markdown": {"title": "Yearning sql审计平台", "text": "%s"}}`, sv)

	if msg.Key != "" {
		hook = Sign(msg.Key, msg.WebHook)
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
	defer resp.Body.Close()
}

func Sign(secret, hook string) string {
	timestamp := time.Now().UnixNano() / 1e6
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	sign := hmacSha256(stringToSign, secret)
	url := fmt.Sprintf("%s&timestamp=%d&sign=%s", hook, timestamp, sign)
	return url
}
