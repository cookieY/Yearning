# yearning

- https://github.com/chaiyd/docker.git
- 遵循yearning官方,具体查看yearning官方文档

### docker启动时传入相应变量
```
docker run -d -it -p8000:8000 -e MYSQL_USER=root -e MYSQL_ADDR=10.0.0.3:3306 -e MYSQL_PASSWORD=123123 -e MYSQL_DB=Yearning chaiyd/yearning
```

### docker-compose
```
version: '3'
    yearning:
        image: chaiyd/yearning:latest
        environment:
           MYSQL_USER: yearning
           MYSQL_PASSWORD: ukC2ZkcG_ZTeb
           MYSQL_ADDR: mysql
           MYSQL_DB: yearning
        ports:
           - 8000:8000
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
           - --collation-server=utf8mb4_unicode_ci
        volumes:
           - mysql-data:/var/lib/mysql
volumes:
  mysql-data:

# 默认账号：admin，默认密码：Yearning_admin
```
