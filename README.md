<p align="center">
        <img width="300" src="http://oy0f4k5qi.bkt.clouddn.com/git_logo2.svg">
</p>

# Yearning SQL审核平台

![](https://img.shields.io/badge/build-passing-brightgreen.svg)  
![](https://img.shields.io/badge/vue.js-2.5.0-brightgreen.svg) 
![](https://img.shields.io/badge/iview-2.8.0-brightgreen.svg?style=flat-square) 
![](https://img.shields.io/badge/python-3.6-brightgreen.svg)
![](https://img.shields.io/badge/Django-1.11.3-brightgreen.svg)

基于Inception的整套sql审核平台解决方案。

## Feature 功能：
- SQL可视化自动生成
 - INDEX 索引语句
 - ALTER 更改表结构语句
- 数据库字典
  - 数据表字段一键生成
- SQL审核
    - 流程化工单
    - SQL语句检测
    - SQL语句执行
    - SQL回滚
    - 站内信通知
    - todoList
    - 钉钉webhook机器人通知    
- 历史审核记录
- 用户权限及管理

## Environment 环境

- Python 3.6

- Vue.js 2.5

- Django 1.11.3

## Install 安装及更新日志
- [Yearning使用及安装文档](https://cookiey.github.io/Yearning-document/)

- 体验及快速测试安装(docker)

```
docker run -it -d -p 80:80 -p 8000:8000 -e "HOST=宿主机ip" registry.cn-hangzhou.aliyuncs.com/cookie/yearning:v0.0.2 

初始账号: admin  密码: Yearning_admin
```
注意: 

由于目前镜像并没有将数据库数据存放目录挂载到宿主机所以不建议在正式环境中使用docker

建议在使用前评估及测试中使用

## About 联系方式
   
   QQ群:103674679
   
   E-mail: im@supermancookie.com

## Snapshot 效果展示

- Login

![login -w1200](http://oy0f4k5qi.bkt.clouddn.com/logo.png)


- Dashboard

![](http://oy0f4k5qi.bkt.clouddn.com/index.png)


- 表结构提交页面

![](http://oy0f4k5qi.bkt.clouddn.com/table.png)

- SQL提交页面

![](http://oy0f4k5qi.bkt.clouddn.com/sql.png)

- 工单页面
![](http://oy0f4k5qi.bkt.clouddn.com/order.png)


## License

- MIT

2018 © Cookie


