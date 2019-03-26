'''

About connection Database

2017-11-23

cookie
'''

from libs.cryptoAES import cryptoAES
from settingConf import settings
import pymssql


class MSSQL(object):
    def __init__(self, ip=None, user=None, password=None, port=None, db=None):
        self.AES = cryptoAES(settings.SECRET_KEY)
        self.ip = ip
        self.user = user
        self.db = db
        self.port = int(port)
        self.con = object
        try:
            self.password = self.AES.decrypt(password)
        except ValueError:
            self.password = password

    @staticmethod
    def addDic(theIndex, word, value):
        theIndex.setdefault(word, []).append(value)

    def __enter__(self):
        self.con = pymssql.connect(
            server=self.ip,
            user=self.user,
            password=self.password,
            database=self.db,
            port=self.port,
            charset='utf8'
        )
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.con.close()

    def search(self, sql=None):
        data_dict = []
        with self.con.cursor(as_dict=True) as cursor:
            sqllist = sql
            cursor.execute(sqllist)
            result = cursor.fetchall()
            for field in cursor.description:
                data_dict.append(
                    {'title': field[0], "key": field[0], "width": 200})
            len = cursor.rowcount
        return {'data': result, 'title': data_dict, 'len': len}

    def showtable(self, table_name):
        with self.con.cursor() as cursor:
            sqllist = '''
                    SELECT  
                     a.name AS COLUMN_NAME, 
                    b.name AS DATA_TYPE, 
                    isnull(g.[value],'') AS COLUMN_COMMENT,
                    case when a.colorder=1 then isnull(f.value,'') else '' end AS TABLE_COMMENT
                    FROM syscolumns a 
                    left join systypes b on a.xtype=b.xusertype 
                    inner join sysobjects d on a.id=d.id and d.xtype='U' and d.name<>'dtproperties' 
                    left join syscomments e on a.cdefault=e.id 
                    left join sys.extended_properties g on a.id=g.major_id and a.colid=g.minor_id 
                    left join sys.extended_properties f on d.id=f.major_id and f.minor_id =0 
                    where d.name='%s' 
                    order by a.id,a.colorder;
                    ''' % table_name
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

    # 获取表结构
    def gen_alter(self, table_name):
        with self.con.cursor() as cursor:
            sqllist ='''
                    SELECT 
                    a.name AS COLUMN_NAME, 
                    b.name+'('+cast(COLUMNPROPERTY(a.id,a.name,'PRECISION') as varchar)+ 
                    CASE isnull(COLUMNPROPERTY(a.id,a.name,'Scale'),0)
                        WHEN 0 THEN ')'
                        ELSE ','+cast(isnull(COLUMNPROPERTY(a.id,a.name,'Scale'),0) as varchar)+')'
                    END
                    AS DATA_TYPE, 
                    case when a.isnullable=1 then 'YES'else 'NO' end AS NULLABLE,  
                    case when exists(SELECT 1 FROM sysobjects where xtype='PK' and name in (
                    SELECT name FROM sysindexes WHERE indid in(
                    SELECT indid FROM sysindexkeys WHERE id = a.id AND colid=a.colid 
                    ))) then '√' else '' end AS PRIMARY_KEY, 
                    isnull(e.text,'') AS DEFAULT_VALUE, 
                    isnull(g.[value],'')  AS COLUMN_COMMENT
                    FROM syscolumns a 
                    left join systypes b on a.xtype=b.xusertype 
                    left join sysobjects d on a.id=d.id and d.xtype='U' and d.name<>'dtproperties' 
                    left join syscomments e on a.cdefault=e.id 
                    left join sys.extended_properties g on a.id=g.major_id and a.colid=g.minor_id 
                    left join sys.extended_properties f on d.id=f.major_id and f.minor_id =0 
                    where d.name='%s'
                    order by a.id,a.colorder
                    ''' % table_name

            cursor.execute(sqllist)
            result = cursor.fetchall()
            td = [
                {
                    'Field': i[0],
                    'Type': i[1],
                    'Null': i[2],
                    'Key': i[3],
                    'Default': i[4],
                    'Extra': i[5]
                } for i in result
            ]
        return td
    # 获取表索引
    def index(self, table_name):
        with self.con.cursor() as cursor:
            get_index_sql='''           
                    SELECT index_name,
                            is_unique,
                           index_description,
                           (LEFT(ind_col, LEN(ind_col)-1)
                           + case when include_col IS NOT NULL
                               THEN ' INCLUDE (' + LEFT(include_col, LEN(include_col)-1) + ')'
                             else '' end) AS index_col
                    FROM
                    (SELECT i.name AS index_name, i.is_unique as is_unique, 
                            (SELECT CONVERT(varchar(max), 
                             case when i.index_id = 1 then 'clustered' else 'nonclustered' end  
                             + case when i.ignore_dup_key <>0 then ', ignore duplicate keys' else '' end  
                             + case when i.is_unique <>0 then ', unique' else '' end  
                             + case when i.is_hypothetical <>0 then ', hypothetical' else '' end  
                             + case when i.is_primary_key <>0 then ', primary key' else '' end  
                             + case when i.is_unique_constraint <>0 then ', unique key' else '' end
                             + case when s.auto_created <>0 then ', auto create' else '' end  
                             + case when s.no_recompute <>0 then ', stats no recompute' else '' end  
                             + ' located on ' + ISNULL(name, '')
                             + case when i.has_filter = 1 then ', filter={' + i.filter_definition + '}' else '' end)
                         FROM sys.data_spaces
                         WHERE data_space_id = i.data_space_id ) AS 'index_description',
                         (SELECT INDEX_COL(OBJECT_NAME(i.object_id), i.index_id, key_ordinal),
                                 CASE WHEN is_descending_key = 1 THEN N'(-)' ELSE N'' END + ','
                          FROM sys.index_columns
                          WHERE object_id = i.object_id 
                            AND index_id = i.index_id 
                            AND key_ordinal <> 0
                          ORDER BY key_ordinal FOR XML PATH('')) AS ind_col,
                             (SELECT col.name + ','
                              FROM sys.index_columns inxc 
                              JOIN sys.columns col
                                ON col.object_id = inxc.object_id
                                   AND col.column_id = inxc.column_id
                              WHERE inxc.object_id = i.object_id 
                                    AND inxc.index_id = i.index_id
                                    AND inxc.is_included_column = 1
                              FOR XML PATH('')) AS include_col
                    FROM sys.indexes i
                    JOIN sys.stats s
                    ON i.object_id = s.object_id
                      AND i.index_id = s.stats_id  
                    WHERE i.object_id = object_id('%s')) Ind
                    ORDER BY index_name
                ''' % table_name
            cursor.execute(get_index_sql)
            result = cursor.fetchall()
            di = [
                {
                    'Non_unique': '是',
                    'key_name': i[0],
                    'column_name': i[3],
                    'index_type': i[2]
                }
                if i[1] == 1
                else
                {
                    'Non_unique': '否',
                    'key_name': i[0],
                    'column_name': i[3],
                    'index_type': i[2]
                }
                for i in result
            ]
            return di

    def baseItems(self, sql=None):

        with self.con.cursor() as cursor:
            cursor.execute(sql)
            result = cursor.fetchall()
            data = [c for i in result for c in i]
            return data

    def query_info(self, sql=None):
        with self.con.cursor(as_dict=True) as cursor:
            cursor.execute(sql)
            result = cursor.fetchall()
        return result
