import json
import logging
import re
from libs import util
from rest_framework.response import Response
from django.http import  HttpResponse
from libs import baseview
from libs import con_database
from core.models import DatabaseList,SqlAdvisor
from libs import  call_advisor
from hashlib import md5

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

conf = util.conf_path()


class advisorsql(baseview.BaseView):

    '''
    :argument   sql查询接口, 过滤非查询语句并返回查询结果。
                可以自由limit数目 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    def post(self, request, args=None):
        try:
            analysis_result = json.loads(request.data['analysis_result'])
            sql = json.loads(request.data['sql'])
            user = request.data['user']
            type = request.data['type']
            id = request.data['id']
            dbinfo_name=request.data['basename']
            # redis_conn = redis.Redis(host=REDIS_HOST, port=REDIS_PORT, db=0)
            querykey = 'sqladvisor_' + md5(dbinfo_name + '_' + sql).hexdigest()
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                SqlAdvisor.objects.get_or_create(
                    username=user,
                    datcreate_time=util.date(),
                    analysis_result=analysis_result,
                    sql=sql,
                    type=type,
                    dbinfo_name=dbinfo_name,
                    db_id=id
                    )
                return Response('已提交')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


    def put(self,request,args=None):
        sql = request.data['sql']
        check = str(sql).strip().split(';\n')
        if check[-1].strip().lower().startswith('s') != 1:
            return Response({'error': '只支持查询功能或删除不必要的空白行！'})
        else:
            address = json.loads(request.data['address'])
            _c = DatabaseList.objects.filter(id=address['id']).first()
            try:
                info = {
                    'host': _c.ip,
                    'user': _c.username,
                    'password': _c.password,
                    'db': address['basename'],
                    'port': _c.port
                    }
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    with call_advisor.Sqladvisor(LoginDic=info) as analysis:
                        res = analysis.Check(sql)
                        return Response({'result': res, 'status': 200})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response({'status': '500'})