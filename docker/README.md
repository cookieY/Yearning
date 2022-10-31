# yearning

- 具体查看yearning官方文档
- 请在启动前先执行 yearning install 进行初始化

### docker启动时传入相应变量
```
docker run -d -it \
           -p8000:8000 -e IS_DOCKER=is_docker \
           -e SECRET_KEY=dbcjqheupqjsuwsm \
           -e MYSQL_USER=root \
           -e MYSQL_ADDR=10.0.0.3:3306 \
           -e MYSQL_PASSWORD=123123 \
           -e MYSQL_DB=Yearning \
           chaiyd/yearning
```

### docker-compose
- 第一次安装，取消下列compose 中的注释进行初始化
  - `command: /bin/bash -c "./Yearning install"`
- 升级使用
  - `command: /bin/bash -c "./Yearning migrate"`
- 重置admin密码
  - `command: /bin/bash -c "./Yearning reset_super"`

### docker tag
  - https://hub.docker.com/r/chaiyd/yearning/tags

### docker-compose
```
version: '3'

services:
    yearning:
        image: chaiyd/yearning:latest
        environment:
           MYSQL_USER: yearning
           MYSQL_PASSWORD: ukC2ZkcG_ZTeb
           MYSQL_ADDR: mysql
           MYSQL_DB: yearning
           SECRET_KEY: dbcjqheupqjsuwsm
           IS_DOCKER: is_docker
        ports:
           - 8000:8000
        # 首次使用请先初始化
        # command: /bin/bash -c "./Yearning install && ./Yearning run"
        depends_on:
           - mysql
        restart: always

    mysql:
        image: mysql:5.7
        environment:
           MYSQL_ROOT_PASSWORD: ukC2ZkcG_ZTeb
           MYSQL_DATABASE: yearning
           MYSQL_USER: yearning
           MYSQL_PASSWORD: ukC2ZkcG_ZTeb
        command:
           - --character-set-server=utf8mb4
           - --collation-server=utf8mb4_general_ci
        volumes:
           - ./data/mysql:/var/lib/mysql

# 默认账号：admin，默认密码：Yearning_admin
```
