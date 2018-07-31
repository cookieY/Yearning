#/bin/bash
chown -R mysql:mysql /var/lib/mysql /var/run/mysqld
/usr/bin/mysqld_safe &
sleep 3

if [ ! -d "/var/lib/mysql/Yearning" ]; then
  mysql -e "create database Yearning character set 'utf8' collate 'utf8_general_ci' ;"
  python3 manage.py makemigrations
  python3 manage.py migrate
  echo "from core.models import Account; Account.objects.create_user(username='admin', password='"$PASSWORD"', group='admin',is_staff=1,auth_group='admin')" | python3 manage.py shell 
  echo "from core.models import grained;grained.objects.get_or_create(username='admin', permissions={'ddl': '1', 'ddlcon': [], 'dml': '1', 'dmlcon': [], 'dic': '1', 'diccon': [], 'dicedit': '0', 'query': '1', 'querycon': [], 'user': '1', 'base': '1', 'dicexport': '0', 'person': []})" | python3 manage.py shell
  echo "from core.models import globalpermissions; globalpermissions.objects.get_or_create(authorization='global', inception={'host': '', 'port': '', 'user': '', 'password': '', 'back_host': '', 'back_port': '', 'back_user': '', 'back_password': ''}, ldap={'type': '', 'host': '', 'sc': '', 'domain': '', 'user': '', 'password': ''}, message={'webhook': '', 'smtp_host': '', 'smtp_port': '', 'user': '', 'password': '', 'to_user': '', 'mail': False, 'ding': False}, other={'limit': '', 'con_room': ['AWS', 'Aliyun', 'Own', 'Other'], 'foce': '', 'multi': False, 'query': False, 'sensitive_list': [], 'sensitive': ''})" | python3 manage.py shell 
fi

sed -i "s/ipaddress =.*/ipaddress=$HOST/" deploy.conf
/usr/sbin/nginx
/opt/Yearning/install/inception/bin/Inception --defaults-file=/opt/Yearning/install/inception/bin/inc.cnf &
python3 manage.py runserver 0.0.0.0:8000
