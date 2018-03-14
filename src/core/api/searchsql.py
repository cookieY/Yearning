import json
import re
import logging
from libs import util
from rest_framework.response import Response
from libs import baseview
from libs import con_database
from core.models import DatabaseList

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

conf = util.conf_path()


class serach(baseview.BaseView):

    '''
    sql查询接口, 过滤非查询语句并返回查询结果
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
                    '''
                    check可能是多条查询语句，取最后一条查询语句执行
                    '''
                    query_sql = replace_limit(check[-1].strip(), int(conf.limit))
                    dataset = f.search(sql=query_sql)
                    return Response(dataset)
            except Exception as e:
                return Response({'error': str(e)})


def replace_limit(sql, limit):
    """
    替换sql中所有limit的字符

    依次查找limit关键字，处理一个limit用指定字符替换
    全部处理完在把特殊字符替换会limit
    :param sql:
    :param limit:
    :return:
    """
    special_flag = 'f-*jin-*du-*yearning'
    special_flag_keyword = 'k-*jin-*du-*yearning'  #  mysql 关键字

    def fun(new_sql):
        """
        limit  将limit替换程 l-*jin-*fu-*imit
        :return:
        """
        upper_sql = new_sql.upper()
        start_index = upper_sql.find('LIMIT') + len('LIMIT')
        end_index = start_index

        for i in range(start_index, len(upper_sql)):
            if bool(re.match(r'^[0-9]|,| ', upper_sql[i])):
                end_index += 1
            else:
                break

        limit_str = upper_sql[start_index:end_index]
        limit_str = limit_str.strip()

        # 处理字段带有limit字符的字段
        if len(limit_str) < 1:
            new_sql = new_sql.replace(
                new_sql[start_index - len('LIMIT'):start_index], special_flag, 1
            )
            return new_sql

        # 输入limit值大于默认limit值就进行替换成默认limit值
        if ',' in limit_str:
            offsets = limit_str.split(',')
            if int(offsets[-1]) > limit:
                limit_str = '{}, {}'.format(offsets[0], limit)
        else:
            if int(limit_str) > limit:
                limit_str = '{}'.format(limit)

        limit_str = ' ' + limit_str + ' '
        new_sql = new_sql.replace(
            new_sql[start_index:end_index], limit_str, 1
        )
        new_sql = new_sql.replace(
            new_sql[start_index - len('LIMIT'):start_index], special_flag, 1
        )
        return new_sql

    # 处理字段带有limit关键字
    field_limit = '\`limit\`'
    sql = re.sub(field_limit, special_flag_keyword, sql, re.IGNORECASE)

    # 原sql没有limit 在最后加上 limit
    if re.search(r'limit\s.*\d.*', sql, re.IGNORECASE) is None:
        sql = sql.rstrip(';') + ' limit %s' % int(limit) + ';'

    # 分析limit语句
    while bool(re.search('limit', sql, re.IGNORECASE)):
        sql = fun(sql)

    # 替换回limit语句
    sql = sql.replace(special_flag, 'limit')
    sql = sql.replace(special_flag_keyword, '`limit`')
    return sql
