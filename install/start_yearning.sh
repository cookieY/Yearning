/usr/sbin/nginx
cd /opt/Yearning/src
sed -i "s/ipaddress =.*/ipaddress=$HOST/" deploy.conf
sed -i "s/address =.*/address=$MYSQL_ADDR/" deploy.conf
sed -i "s/username =.*/username=$MYSQL_USER/" deploy.conf
sed -i "s/password =.*/password=$MYSQL_PASSWORD/" deploy.conf
gunicorn settingConf.wsgi:application -b 0.0.0.0:8000 -w 2
