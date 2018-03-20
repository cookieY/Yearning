'''

About connection Database

2017-11-23

cookie
'''

import pymysql


class SQLgo(object):
    def __init__(self, ip=None, user=None, password=None, db=None, port=None):
        self.ip = ip
        self.user = user
        self.password = password
        self.db = db
        self.port = int(port)
        self.con = object

    @staticmethod
    def addDic(theIndex, word, value):
        theIndex.setdefault(word, []).append(value)

    def __enter__(self):
        self.con = pymysql.connect(
            host=self.ip,
            user=self.user,
            passwd=self.password,
            db=self.db,
            charset='utf8mb4',
            port=self.port
            )
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.con.close()

    def execute(self, sql=None):
        with self.con.cursor() as cursor:
            sqllist = sql
            cursor.execute(sqllist)
            result = cursor.fetchall()
            self.con.commit()
        return result

    def search(self, sql=None):
        data_dict=[]
        id = 0
        with self.con.cursor(cursor=pymysql.cursors.DictCursor) as cursor:
            sqllist = sql
            cursor.execute(sqllist)
            result = cursor.fetchall()
            for field in cursor.description:
                if id == 0:
                    data_dict.append({'title': field[0], "key": field[0], "fixed": "left", "width": 150})
                    id += 1
                else:
                    data_dict.append({'title': field[0], "key": field[0], "width": 200})
            len = cursor.rowcount
        return {'data': result, 'title': data_dict, 'len': len}

    def dic_data(self, sql=None):
        with self.con.cursor(cursor=pymysql.cursors.DictCursor) as cursor:
            sqllist = sql
            cursor.execute(sqllist)
            result = cursor.fetchall()
        return result

    def showtable(self, table_name):
        with self.con.cursor() as cursor:
            sqllist = '''
                    select aa.COLUMN_NAME,
                    aa.DATA_TYPE,aa.COLUMN_COMMENT, cc.TABLE_COMMENT 
                    from information_schema.`COLUMNS` aa LEFT JOIN 
                    (select DISTINCT bb.TABLE_SCHEMA,bb.TABLE_NAME,bb.TABLE_COMMENT 
                    from information_schema.`TABLES` bb ) cc  
                    ON (aa.TABLE_SCHEMA=cc.TABLE_SCHEMA and aa.TABLE_NAME = cc.TABLE_NAME )
                    where aa.TABLE_SCHEMA = '%s' and aa.TABLE_NAME = '%s';
                    ''' % (self.db, table_name)
            cursor.execute(sqllist)
            result = cursor.fetchall()
            td = [
                {
                    'Field': i[0], 
                    'Type': i[1],
                    'Extra': i[2],
                    'TableComment': i[3]
                } for i in result
                ]
        return td

    def gen_alter(self, table_name):
        with self.con.cursor() as cursor:
            sqllist = '''
                           select aa.COLUMN_NAME,aa.COLUMN_DEFAULT,aa.IS_NULLABLE,
                           aa.COLUMN_TYPE,aa.COLUMN_KEY,aa.COLUMN_COMMENT, cc.TABLE_COMMENT 
                           from information_schema.`COLUMNS` aa LEFT JOIN 
                           (select DISTINCT bb.TABLE_SCHEMA,bb.TABLE_NAME,bb.TABLE_COMMENT 
                           from information_schema.`TABLES` bb ) cc  
                           ON (aa.TABLE_SCHEMA=cc.TABLE_SCHEMA and aa.TABLE_NAME = cc.TABLE_NAME )
                           where aa.TABLE_SCHEMA = '%s' and aa.TABLE_NAME = '%s';
                           ''' % (self.db, table_name)
            cursor.execute(sqllist)
            result = cursor.fetchall()
            td = [
                {
                    'Field': i[0],
                    'Type': i[3],
                    'Null': i[2],
                    'Key': i[4],
                    'Default': i[1],
                    'Extra': i[5],
                    'TableComment': i[6]
                } for i in result
            ]
        return td

    def basename(self):
        with self.con.cursor() as cursor:
            cursor.execute('show databases')
            result = cursor.fetchall()
            data = [c for i in result for c in i]
            return data

    def index(self, table_name):
        with self.con.cursor() as cursor:
            cursor.execute('show keys from %s' % table_name)
            result = cursor.fetchall()
            di = [
                {
                    'Non_unique': '是', 
                    'key_name': i[2], 
                    'column_name': i[4], 
                    'index_type': i[10]
                }
                if i[1] == 0
                else 
                {
                    'Non_unique': '否', 
                    'key_name': i[2], 
                    'column_name': i[4], 
                    'index_type': i[10]
                }
                for i in result 
                ]

            dic = {}
            c = []
            for i in di:
                self.addDic(dic, i['key_name'], i['column_name'])
            for t in dic:
                """
                初始化第一个value
                将value 数据变为字符串
                转为字典对象数组
                """
                str1 = dic[t][0]

                for i in range(1, len(dic[t])):
                    str1 = str1 + ',' + dic[t][i]

                temp = {}
                for g in di:
                    if t == g['key_name']:
                        temp.setdefault('Non_unique', g['Non_unique'])
                        temp.setdefault('index_type', g['index_type'])
                temp.setdefault('column_name', str1)
                temp.setdefault('key_name', t)
                c.append(temp)
            return c

    def tablename(self):
        with self.con.cursor() as cursor:
            cursor.execute('show tables')
            result = cursor.fetchall()
            data = [c for i in result for c in i]
            return data