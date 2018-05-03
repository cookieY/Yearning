#!/bin/bash
#
# Description: Yearning restart/stop/show/install script
# Date: 2018-02-26
# Author: Pengdongwen
# Blog: www.ywnds.com

# Network
ping -c 1 -W 3 www.baidu.com &> /dev/null
if [ ! $? = 0 ];then
  echo "Cannot be networked"
  exit 1
fi

echo "
-------------------------------------------
                                          |
  1: Restart all services                 |
  2: Stop all services                    |
  3: Show all service information         |
  4: One-click Installation Yearning      |
                                          |
-------------------------------------------
"

# Set PATH Variables
export PATH=/usr/local/sbin:/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/bin:/root/bin

# Set output color
COLUMENS=80
SPACE_COL=$[ $COLUMENS-15 ]
VERSION=`uname -r | awk -F'.' '{print $1}'`
 
RED='\033[1;5;31m'
GREEN='\033[1;32m'
NORMAL='\033[0m'
 
success() {
  REAL_SPACE=$[ $SPACE_COL - ${#1} ]
  for i in `seq 1 $REAL_SPACE`; do
      echo -n " "
  done
  echo -e "[ ${GREEN}SUCCESS${NORMAL} ]"
}

failure() {
  REAL_SPACE=$[ $SPACE_COL - ${#1} ]
  for i in `seq 1 $REAL_SPACE`; do
      echo -n " "
  done
  echo -e "[ ${RED}FAILURE${NORMAL} ]"
  exit 1
}

help() {
  echo "Please enter a valid serial number" 
}

install() {
# 01
Data="01) 安装依赖包, 请等待..."
echo -n $Data
rm -fr /var/run/yum.pid &> /dev/null
rm -fr /var/tmp/* &> /dev/null
yum install -y perl-IO-Socket-SSL perl-DBD-MySQL perl-Time-HiRes perl-TermReadKey perl-IO-Socket-SSL &> /dev/null
if [ ! $? = 0 ];then
    failure "$Data"
fi
yum install -y epel-release wget gcc openssl-devel git python-pip net-tools &> /dev/null
if [ ! $? = 0 ];then
    failure "$Data"
fi
yum install -y zlib zlib-devel tar gzip bzip2 xz zip &>/dev/null
if [ ! $? = 0 ];then
    failure "$Data"
fi
if [ -e /tmp/percona-toolkit-2.2.20-1.noarch.rpm ];then
    cd /tmp
    yum localinstall -y percona-toolkit-2.2.20-1.noarch.rpm &> /dev/null
else
    cd /tmp
    wget https://www.percona.com/downloads/percona-toolkit/2.2.20/RPM/percona-toolkit-2.2.20-1.noarch.rpm &> /dev/null
    yum localinstall -y percona-toolkit-2.2.20-1.noarch.rpm &> /dev/null
fi
if [ ! $? = 0 ];then
    failure "$Data"
else
    success "$Data"
fi

# 02
Data="02) 安装Nginx, 请等待..."
echo -n $Data
yum install -y nginx &>/dev/null && success "$Data" || failure "$Data"

# 03
Data="03) 安装MySQL, 请等待..."
echo -n $Data
if [ $VERSION = 2 ];then
echo '
[mysql56-community]
name=MySQL 5.6 Community Server
baseurl=http://repo.mysql.com/yum/mysql-5.6-community/el/6/$basearch/
enabled=1
gpgcheck=0' > /etc/yum.repos.d/mysql.repo
else
echo '
[mysql56-community]
name=MySQL 5.6 Community Server
baseurl=http://repo.mysql.com/yum/mysql-5.6-community/el/7/$basearch/
enabled=1
gpgcheck=0' > /etc/yum.repos.d/mysql.repo
fi

which mysqld &> /dev/null
if [ $? = 0 ];then
  echo ""
  read -p "MySQL/MariaDB已经存在,是否卸载和删除所有数据后重新安装[y/n]: " SELECT
  case $SELECT in
    y|Y)
      Data="卸载MySQL中, 请等待..."
      echo -n $Data
      yum remove mysql-community-* MariaDB* -y &> /dev/null && success "$Data" || failure "$Data" 
      rm -fr /tmp/mysql_back &> /dev/null
      mv /var/lib/mysql /tmp/mysql_back &> /dev/null
      Data="安装MySQL中, 请等待..."
      echo -n $Data
      yum install -y mysql-community-server &>/dev/null && success "$Data" || failure "$Data" 
      ;;
    n|N)
      V=1  
      ;;
    *)
      exit 1
  esac
else
  yum install -y mysql-community-server &>/dev/null && success "$Data" || failure "$Data" 
fi

# 04
Data="04) 安装Python 3.6, 请等待..."
echo -n $Data
which python3.6 &> /dev/null
if [ $? = 0 ]; then
  success "$Data"
else
  cd /opt && rm -fr Python-3.6.4.tar.xz &> /dev/null
  wget https://www.python.org/ftp/python/3.6.4/Python-3.6.4.tar.xz &> /dev/null 
  tar xvf Python-3.6.4.tar.xz &> /dev/null && cd Python-3.6.4
  if [ $? = 0 ];then 
    ./configure &> /dev/null && make &> /dev/null && make install &>/dev/null && success "$Data" || failure "$Data"
  fi
fi

# 05
Data="05) 下载Yearning包, 请等待..."
echo -n $Data
cd /opt && rm -fr Yearning_back &> /dev/null && mv Yearning Yearning_back &> /dev/null
git clone https://github.com/cookieY/Yearning.git &>/dev/null || failure "$Data"
cd /opt/Yearning/src &> /dev/null
pip3 install -r requirements.txt &>/dev/null && success "$Data" || failure "$Data" 

# 06
Data="06) 复制Yearning文件, 请等待..."
echo -n $Data
ps aux | grep runserver | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
rm -fr /usr/share/nginx/html/* &> /dev/null
yes | cp -fr /opt/Yearning/install/connections.py /usr/local/lib/python3.6/site-packages/pymysql/ &> /dev/null 
yes | cp -fr /opt/Yearning/install/cursors.py /usr/local/lib/python3.6/site-packages/pymysql/ &> /dev/null
yes | cp -fr /opt/Yearning/webpage/dist/* /usr/share/nginx/html/ &>/dev/null && success "$Data" || failure "$Data"

# 07
Data="07) 启动Ningx, 请等待..."
echo -n $Data
if [ $VERSION = 2 ];then
  service nginx restart &>/dev/null && success "$Data" || failure "$Data"
else
  systemctl restart nginx &>/dev/null && success "$Data" || failure "$Data"
fi  

# 08
Data="08) 启动MySQL, 请等待..."
echo -n $Data
if [ $VERSION = 2 ];then
  service mysqld restart &>/dev/null && success "$Data" || failure "$Data"
else
  systemctl restart mysqld &>/dev/null && success "$Data" || failure "$Data" 
fi  

# 09
if [ "$V" = 1 ];then
read -p "09) 输入MySQL的root用户密码: " MYSQLPASSWORD
if [ -z $MYSQLPASSWORD ];then
   echo "输入不能为空,请再次输入"
   read -p "输入MySQL的root用户密码: " MYSQLPASSWORD
fi
else
read -p "09) 设置MySQL的root用户密码: " MYSQLPASSWORD
if [ -z $MYSQLPASSWORD ];then
   echo "输入不能为空,请再次输入"
   read -p "设置MySQL的root用户密码: " MYSQLPASSWORD
fi
fi

mysql -uroot -e "grant all on *.* to root@localhost identified by '${MYSQLPASSWORD}'; flush privileges;" &> /dev/null
if [ $? = 0 ];then
  mysql -uroot -p"$MYSQLPASSWORD" -e "create database if not exists Yearning charset utf8;" &> /dev/null
else
  mysql -uroot -p"$MYSQLPASSWORD" -e "create database if not exists Yearning charset utf8;" &> /dev/null
fi

# 10
ADDRESS=`netstat -anplt | grep "sshd" | grep ESTABLISHED | awk '{print $4}' | awk -F':' '{print $1}' | head -n1`
if [ -z $ADDRESS ];then
  ADDRESS="127.0.0.1"
  read -p "10) 输入你要通过浏览器访问的IP地址[默认: $ADDRESS]: " ADDRESS 
  if [ -z $ADDRESS ];then
    ADDRESS=`netstat -anplt | grep "sshd" | grep ESTABLISHED | awk '{print $4}' | awk -F':' '{print $1}' | head -n1`
    if [ -z $ADDRESS ];then
      ADDRESS="127.0.0.1"
    fi
  fi
else
  read -p "10) 输入你要通过浏览器访问的IP地址[默认: $ADDRESS]: " ADDRESS 
  ADDRESS=`netstat -anplt | grep "sshd" | grep ESTABLISHED | awk '{print $4}' | awk -F':' '{print $1}' | head -n1`
fi
yes | cp -fr deploy.conf.template deploy.conf &> /dev/null
cd /opt/Yearning/src && sed -i "s/backuppassword =.*/backuppassword = $MYSQLPASSWORD/" deploy.conf &> /dev/null
cd /opt/Yearning/src && sed -i "s/ipaddress = .*/ipaddress = ${ADDRESS}:8000/" deploy.conf &> /dev/null
cd /opt/Yearning/src && sed -i "s/password =.*/password = $MYSQLPASSWORD/" deploy.conf &> /dev/null 

# 11
Data="11) 初始化Yeanring表, 请等待..."
echo -n $Data
cd /opt/Yearning/src
python3 manage.py makemigrations &> /dev/null && python3 manage.py migrate &> /dev/null && success "$Data" || failure "$Data"

# 12
read -p "12) 设置Yearning的admin用户密码[大于6位, 不支持特殊符号]: " PASSWORD
if [ -z $PASSWORD ];then
   echo -e "输入不能为空,请再次输入[大于6位, 不支持特殊符号]"
   read -p "设置Yearning的admin用户密码[大于6位, 不支持特殊符号]: " PASSWORD
fi
cd /opt/Yearning/src
echo "from core.models import Account; Account.objects.create_user(username='admin', password="$PASSWORD", group='admin',is_staff=1)" | python3 manage.py shell &> /dev/null
echo "from core.models import grained;grained.objects.get_or_create(username='admin', permissions={'ddl': '1', 'ddlcon': [], 'dml': '1', 'dmlcon': [], 'dic': '1', 'diccon': [], 'dicedit': '0', 'query': '1', 'querycon': [], 'user': '1', 'base': '1', 'dicexport': '0', 'person': []})" | python3 manage.py shell &> /dev/null

# 13
Data="13) 启动Inception, 请等待..."
echo -n $Data
cd /opt/Yearning/install/ && rm -fr inception && tar xvf inception.tar &> /dev/null
ps aux | grep Inception | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
/opt/Yearning/install/inception/bin/Inception --defaults-file=/opt/Yearning/install/inception/bin/inc.cnf &> /dev/null & 
if [ $? = 0 ];then
  success "$Data" 
else
  failure "$Data"
fi

# 14
Data="14) 启动Yearning, 请等待..."
echo -n $Data
cd /opt/Yearning/src
ps aux | grep runserver | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
python3 manage.py runserver 0.0.0.0:8000 &> /dev/null &
if [ $? = 0 ];then
  success "$Data" 
else
  failure "$Data"
fi
}

restart() {
  Data="01) Restart Nginx"
  echo -n $Data
  if [ $VERSION = 2 ];then
    service nginx restart &>/dev/null && success "$Data" || failure "$Data"
  else
    systemctl restart nginx &>/dev/null && success "$Data" || failure "$Data"
  fi  
  
  Data="02) Restart MySQL"
  echo -n $Data
  if [ $VERSION = 2 ];then
    service mysqld restart &>/dev/null && success "$Data" || failure "$Data"
  else
    systemctl restart mysqld &>/dev/null && success "$Data" || failure "$Data"
  fi  
  
  Data="03) Restart Inception"
  echo -n $Data
  ps aux | grep Inception | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
  /opt/Yearning/install/inception/bin/Inception --defaults-file=/opt/Yearning/install/inception/bin/inc.cnf &> /dev/null & 
  if [ $? = 0 ];then
    success "$Data" 
  else
    failure "$Data"
  fi
  sleep 1
  
  Data="04) Restart Yearning"
  echo -n $Data
  cd /opt/Yearning/src
  ps aux | grep runserver | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
  python3 manage.py runserver 0.0.0.0:8000 &> /dev/null &
  if [ $? = 0 ];then
    success "$Data" 
  else
    failure "$Data"
  fi
  echo ""
}

stop() {
  Data="01) Stop Nginx"
  echo -n $Data
  if [ $VERSION = 2 ];then
    service nginx stop &>/dev/null && success "$Data" || failure "$Data"
  else
    systemctl stop nginx &>/dev/null && success "$Data" || failure "$Data"
  fi  
  
  Data="02) Stop MySQL"
  echo -n $Data
  if [ $VERSION = 2 ];then
    service mysqld stop &>/dev/null && success "$Data" || failure "$Data"
  else
    systemctl stop mysqld &>/dev/null && success "$Data" || failure "$Data"
  fi  
  
  Data="03) Stop Inception"
  echo -n $Data
  ps aux | grep Inception | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
  if [ $? = 0 ];then
    success "$Data" 
  else
    success "$Data"
  fi
  sleep 1
  
  Data="04) Stop Yearning"
  echo -n $Data
  cd /opt/Yearning/src
  ps aux | grep runserver | grep -v grep | awk '{print $2}' | xargs kill -9 &> /dev/null
  if [ $? = 0 ];then
    success "$Data" 
  else
    success "$Data"
  fi
  echo ""
}

show() {
cat <<END;
----------------------------------------------------------------
Nginx conf     |   /etc/nginx/nginx.conf                       |
Nginx data     |   /usr/share/nginx/html/*                     |
MySQL conf     |   /etc/my.cnf                                 |
MySQL data     |   /var/lib/mysql/*                            |
Inception conf |   /opt/Yearning/install/inception/bin/inc.cnf |
Yearning conf  |   /opt/Yearning/src/deploy.conf               |
Yearning log   |   /opt/Yearning/src/log/*                     |
----------------------------------------------------------------
END
}


read -p "Please select enter a valid sequence number: " NUMBER
echo
case "$NUMBER" in
  1)
    restart
    ;;
  2)
    stop 
    ;;
  3)
    show
    ;;
  4)
    install
    ;;
  *)
    help
    exit 1
esac
