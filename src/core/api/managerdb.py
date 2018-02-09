import logging
import json
from libs import baseview
from libs import con_database
from core.task import grained_permissions
from rest_framework.response import Response
from django.http import HttpResponse
from django.db.models import Count
from core.models import (
    DatabaseList,
    SqlDictionary,
    SqlRecord,
    SqlOrder,
    globalpermissions
)
from libs.serializers import (
    Sqllist
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class managementdb(baseview.SuperUserpermissions):


    '''
    数据库管理相关
    '''


    @grained_permissions
    def get(self, request, args=None):
        try:
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = DatabaseList.objects.aggregate(alter_number=Count('id'))
                start = int(page) * 10 - 10
                end = int(page) * 10
                info = DatabaseList.objects.all()[start:end]
                serializers = Sqllist(info, many=True)
                data = SqlDictionary.objects.all().values('Name')
                data.query.group_by = ['Name']  # 不重复表名
                switch = globalpermissions.objects.filter(authorization='global').first()
                switch_dingding = False
                switch_email = False
                if  switch is not None:
                    if switch.dingding == 1:
                        switch_dingding = True
                    if switch.email == 1:
                        switch_email = True

                return Response(
                    {
                        'page': pagenumber,
                        'data': serializers.data,
                        'diclist': data,
                        'ding_switch': switch_dingding,
                        'mail_switch': switch_email
                    }
                )
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def post(self, request, args=None):
        try:
            data = json.loads(request.data['data'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                DatabaseList.objects.get_or_create(
                    connection_name=data['connection_name'],
                    ip=data['ip'],
                    computer_room=data['computer_room'],
                    username=data['username'],
                    password=data['password'],
                    port=data['port']
                    )
                return Response('ok')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def put(self, request, args=None):
        try:
            ip = request.data['ip']
            user = request.data['user']
            password = request.data['password']
            port = request.data['port']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                with con_database.SQLgo(ip=ip, user=user, password=password, port=port):
                    return Response('连接成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return Response('连接失败!')

    def delete(self, request, args=None):
        try:
            connection_name = request.GET.get('del')
            id = DatabaseList.objects.filter(connection_name=connection_name).first()
            SqlOrder.objects.filter(bundle_id=id.id).delete()
            SqlRecord.objects.filter(name=connection_name).delete()
            DatabaseList.objects.filter(connection_name=connection_name).delete()
            return Response('数据库信息已删除!')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)


class pushpermissions(baseview.SuperUserpermissions):

    '''

    global permissions

    '''

    def post(self, request, args: str = None):
        id = request.data['id']
        type = request.data['type']
        data = globalpermissions.objects.filter(authorization='global').first()
        if data is None:
            globalpermissions.objects.get_or_create(authorization='global', dingding=0, email=0)
        if type == '0':
            globalpermissions.objects.update(dingding=id)
            return Response('钉钉推送设置已更新!')
        else:
            globalpermissions.objects.update(email=id)
            return Response('邮件推送设置已更新!')