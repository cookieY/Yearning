'''

INCEPTION operation

2017-11-23

cookie

'''

from libs import util
import pymysql
import sqlparse

pymysql.install_as_MySQLdb()

conf = util.conf_path()


class Inception(object):
    def __init__(self, LoginDic=None):
        self.__dict__.update(LoginDic)
        self.con = object

    def __enter__(self):
        self.con = pymysql.connect(host=conf.inc_host,
                                   user=conf.inc_user,
                                   passwd=conf.inc_pwd,
                                   port=int(conf.inc_port),
                                   db='',
                                   charset="utf8")
        return self

    def GenerateStatements(self, Sql: str = '', Type: str = '', backup=None):
        if Sql[-1] == ';':
            Sql = Sql.rstrip(';')
        elif Sql[-1] == '；':
            Sql = Sql.rstrip('；')
        if backup is not None and backup != 0:
            InceptionSQL = '''
             /*--user=%s;--password=%s;--host=%s;--port=%s;%s;%s;*/ \
             inception_magic_start;\
             use `%s`;\
             %s; \
             inception_magic_commit;
            ''' % (self.__dict__.get('user'),
                   self.__dict__.get('password'),
                   self.__dict__.get('host'),
                   self.__dict__.get('port'),
                   Type,
                   backup,
                   self.__dict__.get('db'),
                   Sql)
            return InceptionSQL
        else:
            InceptionSQL = '''
                        /*--user=%s;--password=%s;--host=%s;--port=%s;%s;*/ \
                        inception_magic_start;\
                        use `%s`;\
                        %s; \
                        inception_magic_commit;
                       ''' % (self.__dict__.get('user'),
                              self.__dict__.get('password'),
                              self.__dict__.get('host'),
                              self.__dict__.get('port'),
                              Type,
                              self.__dict__.get('db'),
                              Sql)
            return InceptionSQL

    def Execute(self, sql, backup: int):
        if backup == 1:
            Inceptionsql = self.GenerateStatements(Sql=sql, Type='--enable-execute')
        else:
            Inceptionsql = self.GenerateStatements(
                Sql=sql,
                Type='--enable-execute',
                backup='--disable-remote-backup')
        with self.con.cursor() as cursor:
            cursor.execute(Inceptionsql)
            result = cursor.fetchall()
            Dataset = [
                {
                    'ID': row[0],
                    'stage': row[1],
                    'errlevel': row[2],
                    'stagestatus': row[3],
                    'errormessage': row[4],
                    'sql': row[5],
                    'affected_rows': row[6],
                    'sequence': row[7],
                    'backup_dbname': row[8],
                    'execute_time': row[9],
                    'SQLSHA1': row[10]
                } 
                for row in result
            ]
        return Dataset

    def Check(self, sql=None):
        Inceptionsql = self.GenerateStatements(Sql=sql, Type='--enable-check')
        with self.con.cursor() as cursor:
            cursor.execute(Inceptionsql)
            result = cursor.fetchall()
            Dataset = [
                {
                    'ID': row[0], 
                    'stage': row[1], 
                    'errlevel': row[2], 
                    'stagestatus': row[3],
                    'errormessage': row[4], 
                    'sql': row[5], 
                    'affected_rows': row[6],
                    'SQLSHA1': row[10]
                } 
                for row in result 
            ]
        return Dataset

    def oscstep(self, sql=None):
        with self.con.cursor(cursor=pymysql.cursors.DictCursor) as cursor:
            cursor.execute(sql)
            result = cursor.fetchall()
            cursor.close()
        return result

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.con.close()

    @staticmethod
    def BeautifySQL(sql):
        return sqlparse.format(sql, reindent=True, keyword_case='upper')

    def __str__(self):
        return '''

        InceptionSQL Class

        '''
