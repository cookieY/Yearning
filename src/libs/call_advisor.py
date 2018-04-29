'''

SqlAdvisor operation

2017-11-23

cookie

'''
import pymysql
import subprocess
import json
from libs import util
conf = util.conf_path()

#  sql 语法优化
class Sqladvisor(object):
    def __init__(self, LoginDic=None):
        self.__dict__.update(LoginDic)
        self.con = object

    def __enter__(self):
        self.con = pymysql.connect(host=self.__dict__.get('host'),
                                   user=self.__dict__.get('user'),
                                   passwd=self.__dict__.get('password'),
                                   port=int(self.__dict__.get('port')),
                                   db='',
                                   autocommit=True,
                                   charset='utf8mb4')
        return self

    def Check(self,sql_content):
        # sqladvisor  -h %s -u dbuser -p abc.1234 -P 3306 -d dbtest -q "select * from t2 where id=3;" -v 1
        cmd = ''' %s  -h %s -u %s -p %s -P %s -d %s -q " %s " -v  1''' % (
                conf.advisor,
                self.__dict__.get('host'),
                self.__dict__.get('user'),
                self.__dict__.get('password'),
                self.__dict__.get('port'),
                self.__dict__.get('db'),
                sql_content)

        try:
            popen = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, shell=True)
            stdout, stderr = popen.communicate()
            if stderr:
                return  stderr
        except Exception as e:
            # print("Mysql Error %d: %s" % (e.args[0], e.args[1]))
            info="Mysql Error %d: %s" % (e.args[0], e.args[1])
            return info

        return stdout

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.con.close()

    def __str__(self):
        return '''

        SqlAdvirsor Class

        '''