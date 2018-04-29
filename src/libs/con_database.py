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

    def query_one(self, sql):
        with self.con.cursor() as cursor:
            cursor.execute(sql)
            result = cursor.fetchone()
        return result

    def query_multi(self, sql, params=None):
        with self.con.cursor() as cursor:
            cursor.execute(sql, params)
            result = cursor.fetchall()
        return result


    def execute(self, sql=None,args=None):
        with self.con.cursor() as cursor:
            sqllist = sql
            if args:
                cursor.execute(sqllist,args)
            else:
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

    def get_tables(self,schema):
        sql = 'select table_name, engine, table_collation, table_comment from information_schema.tables where ' \
              'table_schema = %s and table_type = \'{0}\''.format(schema,'BASE TABLE')
        return self.query_multi(sql)

    def get_columns(self, schema,table_name):
        sql = 'select column_name, is_nullable, column_key, ' \
              'column_default, column_comment, column_type ' \
              'from information_schema.columns where table_schema = %s and table_name = %s'.format(schema,table_name)
        # params = (schema, table_name)
        return self.query_multi(sql)

    def update_column(self, column, modify_type, table_name, is_execute):
        column_name = column['column_name']
        is_nullable = column['is_nullable']
        column_type = column['column_type']
        column_default = column['column_default']
        column_comment = column['column_comment']

        sql = 'alter table {0} '.format(table_name)
        if 'add' == modify_type:
            sql += 'add column {column} '.format(column=column_name)
        elif 'update' == modify_type:
            sql += 'change column {column} {column} '.format(column=column_name)
        else:
            sql += 'drop column {column}'.format(column=column_name)

        if 'drop' != modify_type:
            sql += '{column_type} '.format(column_type=column_type)

            if 'YES' == is_nullable:
                sql += 'null '
            else:
                sql += 'not null '

            if column_default is not None:
                sql += 'default {default} '.format(default=column_default)

            if column_comment is not None:
                sql += 'comment \'{comment}\''.format(comment=column_comment)

        print(sql)
        if is_execute:
            self.execute(sql)

    def generate_create_sql(self, table_name):
        sql = 'show create table {0}'.format(table_name)
        result = self.query_one(sql)
        return result['Create Table']

    def create_table(self, sql, is_execute):
        print(sql)
        print('\n')
        if is_execute:
            self.execute(sql)