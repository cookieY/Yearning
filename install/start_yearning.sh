#!/bin/sh
sed -i -r "s/ipaddress =.*/ipaddress=$HOST/" /opt/Yearning/src/deploy.conf
sed -i -r "s/address =.*/address=$MYSQL_ADDR/" /opt/Yearning/src/deploy.conf
sed -i -r "s/username =.*/username=$MYSQL_USER/" /opt/Yearning/src/deploy.conf
sed -i -r "s/password =.*/password=$MYSQL_PASSWORD/" /opt/Yearning/src/deploy.conf
/usr/local/bin/gunicorn settingConf.wsgi:application -b 0.0.0.0:8000 -w 2
