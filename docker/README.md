# yearning

- <https://github.com/chaiyd/docker.git>
- 遵循yearning官方,具体查看yearning官方文档
- 请在启动前先执行 yearning install 命令

### Docker启动时传入相应变量

``` shell
docker run -d -it -p8000:8000 -e MYSQL_USER=root -e MYSQL_ADDR=10.0.0.3:3306 -e MYSQL_PASSWORD=123123 -e MYSQL_DB=Yearning chaiyd/yearning
```

### docker-compose

- 初始化
  - docker-compose up -d
  - data/init/yearning.sql
    - 请勿更改文件目录，启动mysql时会自动加载该文件，进行初始化数据库
    - 默认使用库名yearning
  - Yearning install 初始化命令依然可用
- 升级使用
  - `command: /bin/bash -c "./Yearning migrate"`
- 重置admin密码
  - `command: /bin/bash -c "./Yearning reset_super"`

### docker tag

- chaiyd/yearning:latest
  - 默认为正式版，不包含RC 版
- 获取RC版本
  - docker pull chaiyd/yearning:3.0.0-rc10

### docker-compose

```yaml
version: '3'

services:
    yearning:
        image: chaiyd/yearning:latest
        environment:
           MYSQL_USER: yearning
           MYSQL_PASSWORD: ukC2ZkcG_ZTeb
           MYSQL_ADDR: mysql
           MYSQL_DB: yearning
        ports:
           - 8000:8000
        #重置admin密码
        #command: /bin/bash -c "./Yearning reset_super"
        #volumes:
        #   - ./conf.toml:/opt/conf.toml
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
           - ./data/init:/docker-entrypoint-initdb.d/
           - ./data/mysql:/var/lib/mysql

# 默认账号：admin，默认密码：Yearning_admin
```
