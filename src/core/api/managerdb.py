import logging
import json
from libs import baseview
from libs import con_database
from core.task import grained_permissions
from rest_framework.response import Response
from django.http import HttpResponse
from django.db.models import Count
from libs.serializers import Sqllist, Getdingding
from core.models import (
    DatabaseList,
    SqlDictionary,
    SqlRecord,
    SqlOrder,
    globalpermissions,
    grained
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class management_db(baseview.SuperUserpermissions):


    '''

    :argument 数据库管理页面api 接口

    '''


    @grained_permissions
    def get(self, request, args=None):

        '''

        :argument 管理页面数据展示

        :return

                {
                        'page': page_number,
                        'data': serializers.data,
                        'diclist': data,
                        'ding_switch': switch_dingding,
                        'mail_switch': switch_email
                }

        '''

        try:
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                page_number = DatabaseList.objects.aggregate(alter_number=Count('id'))
                start = int(page) * 10 - 10
                end = int(page) * 10
                info = DatabaseList.objects.all()[start:end]
                serializers = Sqllist(info, many=True)
                data = SqlDictionary.objects.all().values('Name')
                data.query.group_by = ['Name']  # 不重复表名
                switch = globalpermissions.objects.filter(authorization='global').first()
                switch_dingding = False
                switch_email = False
                if switch is not None:
                    if switch.dingding == 1:
                        switch_dingding = True
                    if switch.email == 1:
                        switch_email = True

                return Response(
                    {
                        'page': page_number,
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

        '''

        :argument 添加数据库连接信息,并保存至DatabaseList表

        :return: ok

        '''

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

        '''

        :argument 测试数据库连接,并返回测试结果数据

        :return: success or fail

        '''

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

        '''

        :argument 删除数据库连接,并删除改数据库连接相关的工单记录,执行记录，以及权限表等相关所有数据

        :return: success or fail

        '''

        try:
            connection_name = request.GET.get('del')
            con_id = DatabaseList.objects.filter(connection_name=connection_name).first()
            SqlOrder.objects.filter(bundle_id=con_id.id).delete()
            SqlRecord.objects.filter(name=connection_name).delete()
            DatabaseList.objects.filter(connection_name=connection_name).delete()
            per = grained.objects.all().values('username', 'permissions')
            for i in per:
                for c in i['permissions']:
                    if isinstance(i['permissions'][c], list) and c != 'diccon':
                        i['permissions'][c] = list(filter(lambda x: x != connection_name, i['permissions'][c]))
                grained.objects.filter(username=i['username']).update(permissions=i['permissions'])
            return Response('数据库信息已删除!')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)


class push_permissions(baseview.SuperUserpermissions):

    def post(self, request, args: str = None):

        '''

        :argument 邮件与钉钉开关

        '''

        id = request.data['id']
        category = request.data['type']
        data = globalpermissions.objects.filter(authorization='global').first()
        if data is None:
            globalpermissions.objects.get_or_create(authorization='global', dingding=0, email=0)
        if category == '0':
            globalpermissions.objects.update(dingding=id)
            return Response('钉钉推送设置已更新!')
        else:
            globalpermissions.objects.update(email=id)
            return Response('邮件推送设置已更新!')


class dingding(baseview.SuperUserpermissions):

    '''

    dingding 相关

    '''

    def get(self, request, args=None):
        try:
            connection_name = request.GET.get('connection_name')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                data = DatabaseList.objects.filter(connection_name=connection_name).first()
                serializers = Getdingding(data)
                return Response(serializers.data)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def post(self, request, args=None):
        try:
            con_id = request.data['id']
            before = request.data['before']
            after = request.data['after']
            url = request.data['url']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                DatabaseList.objects.filter(id=con_id).update(before=before, after=after, url=url)
                return Response('ok')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
