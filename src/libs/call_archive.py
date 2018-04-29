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
import socket


#  sql 语法优化
class Archvie(object):
    def __init__(self, LoginDic=None):
        self.__dict__.update(LoginDic)
        self.source_con = object
        self.dest_con = object

    def __enter__(self):
        self.source_con = pymysql.connect(host=self.__dict__.get('source_host'),
                                   user=self.__dict__.get('source_user'),
                                   passwd=self.__dict__.get('source_password'),
                                   port=int(self.__dict__.get('soure_port')),
                                   db='',
                                   autocommit=True,
                                   charset='utf8mb4')
        self.dest_con = pymysql.connect(host=self.__dict__.get('dest_host'),
                                   user=self.__dict__.get('dest_user'),
                                   passwd=self.__dict__.get('dest_password'),
                                   port=int(self.__dict__.get('dest_port')),
                                   db='',
                                   autocommit=True,
                                   charset='utf8mb4')
        return self

    def Check(self,archive):
        sql='show table like %s'%(self.dict__.get('source_table'))
        self.source_con.execute(sql)
        source= self.source_con.fetchall()
        if source is None:
            return False





    def Execute(self,archive):

        archive_cmd = "pt-archiver " \
                      "--source h='%s',P='%s',u='%s',p='%s',D='%s',t='%s' " \
                      "--dest h='%s',P='%s',u='%s',p='%s',D='%s',t='%s' " \
                      "--charset=UTF8 --where '%s' --progress 50000 --limit 10000 --txn-size 10000 " \
                      "--bulk-insert --bulk-delete --statistics --purge " % \
                      (archive["source"]["host"], archive["source"]["port"], archive["source"]["user"], archive["source"]["password"], archive["db_source"], archive["table_source"], \
                       archive["dest"]["host"], archive["dest"]["port"], archive["dest"]["user"], archive["dest"]["password"], archive["db_dest"], archive["table_dest"], \
                       archive["archive_condition"])

        p = subprocess.Popen(archive_cmd,stdout=subprocess.PIPE, stderr=subprocess.STDOUT,shell=True)
        lines =[]
        while p.poll() is None:
            line = p.stdout.readline()
            line = line.strip()
            if line:
                print('Subprogram output: [{}]'.format(line))
                lines.append(line)
        if p.returncode == 0:
            print('Subprogram success')
        else:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        return lines

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.source_con.close()
        self.dest_con.close()

    def __str__(self):
        return '''

        SqlArchive Class

        '''