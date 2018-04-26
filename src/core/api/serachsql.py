import json
import logging
import time
import re
import threading
from django.db.models import Count
from libs import util
from rest_framework.response import Response
from libs.serializers import Query_review, Query_list
from libs import baseview
from libs import con_database
from core.models import DatabaseList, Account, querypermissions, query_order

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

conf = util.conf_path()


class search(baseview.BaseView):

    '''
    :argument   sql查询接口, 过滤非查询语句并返回查询结果。
                可以自由limit数目 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    @staticmethod
    def query_callback(timer, user):
        Account.objects.filter(username=user).update(query_per=1)
        try:
            time.sleep(int(timer) * 60)
        except:
            time.sleep(60)
        finally:
            Account.objects.filter(username=user).update(query_per=0)

    def get(self, request, args: str = None):
        if request.GET.get('mode') == 'put':
            data = request.GET.get('timer')
            workid = request.GET.get('workid')
            instructions = request.GET.get('instructions')
            query_order.objects.create(
                work_id=workid,
                instructions=instructions,
                username=request.user,
                timer=data,
                date=util.date()
            )
            t = threading.Thread(target=search.query_callback, args=(data, request.user))
            t.start()
            return Response('')
        else:
            user = Account.objects.filter(username=request.user).first()
            return Response(user.query_per)

    def post(self, request, args=None):
        sql = request.data['sql']
        check = str(sql).strip().split(';\n')
        user = Account.objects.filter(username=request.user).first()
        if user.query_per == 1:
            if check[-1].strip().lower().startswith('s') != 1:
                return Response({'error': '只支持查询功能或删除不必要的空白行！'})
            else:
                render = str(request.data['render'])
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
                        querypermissions.objects.create(
                            work_id=render,
                            username=request.user,
                            statements=query_sql
                        )
                        return Response(data_set)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response({'error': '管理员已将最大limit限制为%s!' % conf.limit})
        else:
            return Response({'error': '已超过申请时限请刷新页面后重新提交申请'})


def replace_limit(sql, limit):

    '''

    :argument 根据正则匹配分析输入信息 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    if sql[-1] != ';':
        sql += ';'
    if sql.startswith('show') != -1:
        return sql
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
    if int(length) <= int(limit):
        return sql
    else:
        sql = re.sub(r'limit\s.*\d.*;', 'limit %s;' % limit)
        return sql


class query_worklf(baseview.SuperUserpermissions):

    def get(self, request, args: str = None):
        page = request.GET.get('page')
        page_number = query_order.objects.aggregate(alter_number=Count('id'))
        start = int(page) * 10 - 10
        end = int(page) * 10
        info = query_order.objects.all().order_by('-id')[start:end]
        serializers = Query_review(info, many=True)
        return Response({'page': page_number, 'data': serializers.data})

    def post(self, request, args: str = None):

        work_id = request.data['workid']
        user = request.data['user']
        data = querypermissions.objects.filter(work_id=work_id,username=user).all().order_by('-id')
        serializers = Query_list(data, many=True)
        return Response(serializers.data)

