import json
import logging
import re
from libs import util
from rest_framework.response import Response
from libs import baseview
from libs import con_database
from core.models import DatabaseList

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

conf = util.conf_path()


class search(baseview.BaseView):

    '''
    :argument   sql查询接口, 过滤非查询语句并返回查询结果。
                可以自由limit数目 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    def post(self, request, args=None):
        sql = request.data['sql']
        check = str(sql).strip().split(';\n')
        if check[-1].strip().lower().startswith('select') != 1:
            return Response({'error': '只支持查询功能或删除不必要的空白行！'})
        else:
            address = json.loads(request.data['address'])
            _c = DatabaseList.objects.filter(id=address['id']).first()
            try:
                with con_database.SQLgo(
                        ip=_c.ip,
                        password=_c.password,
                        user=_c.username,
                        port=_c.port,
                        db=address['basename']
                ) as f:
                    query_sql = replace_limit(check[-1].strip(), conf.limit)
                    data_set = f.search(sql=query_sql)
                    return Response(data_set)
            except Exception as e:
                return Response({'error': str(e)})


def replace_limit(sql, limit):

    '''

    :argument 根据正则匹配分析输入信息 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    if sql[-1] != ';':
        sql += ';'
    sql_re = re.search(r'limit\s.*\d.*;',sql.lower())
    length = ''
    if sql_re is not None:
        c = re.search(r'\d.*', sql_re.group())
        if c is not None:
            if c.group().find(',') != -1:
                length = c.group()[-2]
            else:
                length = c.group().rstrip(';')
    else:
        sql = sql.rstrip(';') + ' limit %s;'%limit
        return sql
    if int(length) < int(limit):
        return sql
    else:
        sql = re.sub(r'limit\s.*\d.*;', 'limit %s;' % limit)
        return sql
