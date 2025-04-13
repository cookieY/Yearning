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
           -e Y_LANG=zh_CN or en_US \
           yeelabs/yearning
```

### docker compose
- 第一次安装，取消下列compose 中的注释进行初始化
  - `command: /bin/bash -c "./Yearning install"`
- 升级使用
  - `command: /bin/bash -c "./Yearning migrate"`
- 重置admin密码
  - `command: /bin/bash -c "./Yearning reset_super"`

### docker tag
  - https://hub.docker.com/r/yeelabs/yearning/tags

### Docker Compose V2
```
services:
    yearning:
        image: yeelabs/yearning:latest
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
          mysql:
           condition: service_healthy
        restart: always

    mysql:
        image: mysql:8.3.0
        environment:
           MYSQL_ROOT_PASSWORD: ukC2ZkcG_ZTeb
           MYSQL_DATABASE: yearning
           MYSQL_USER: yearning
           MYSQL_PASSWORD: ukC2ZkcG_ZTeb
        command:
           - --character-set-server=utf8mb4
           - --collation-server=utf8mb4_general_ci
        healthcheck:
           test: ["CMD", "mysqladmin", "-uroot", "-pukC2ZkcG_ZTeb", "ping", "-h", "localhost"]
           interval: 10s
           timeout: 3s
           retries: 12
        volumes:
           - ./data/mysql:/var/lib/mysql

# 默认账号：admin，默认密码：Yearning_admin
```
