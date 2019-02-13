import json
import logging
import ast
from django.http import HttpResponse
from rest_framework.response import Response
from libs import baseview, con_database, util
from core.task import grained_permissions, set_auth_group
from core.api import serachsql
from django.contrib.auth import authenticate
from core.models import (
    DatabaseList,
    Account
)
from libs.cryptoAES import cryptoAES
from settingConf import settings
from libs.serializers import (
    Area,
    UserINFO
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class addressing(baseview.BaseView):
    '''

    :argument 连接名 库名 表名 字段名 索引名 api接口


    '''

    @grained_permissions
    def put(self, request, args=None):

        if args == 'connection':
            try:
                assigned = set_auth_group(request.user)
                un_init = util.init_conf()
                custom_com = ast.literal_eval(un_init['other'])
                if request.data['permissions_type'] == 'user' or request.data['permissions_type'] == 'own_space':
                    info = DatabaseList.objects.all()
                    con_name = Area(info, many=True).data

                elif request.data['permissions_type'] == 'query':
                    con_name = []
                    permission_spec = set_auth_group(request.user)
                    if permission_spec['query'] == '1':
                        for i in permission_spec['querycon']:
                            con_instance = DatabaseList.objects.filter(
                                connection_name=i).first()
                            if con_instance:
                                con_name.append(
                                    {
                                        'id': con_instance.id,
                                        'connection_name': con_instance.connection_name,
                                        'ip': con_instance.ip,
                                        'computer_room': con_instance.computer_room
                                    })
                    assigned = set_auth_group(request.user)
                    return Response({'assigend': assigned['person'], 'connection': con_name,
                                     'custom': custom_com['con_room']})
                else:
                    con_name = []
                    _type = request.data['permissions_type'] + 'con'
                    permission_spec = set_auth_group(request.user)
                    for i in permission_spec[_type]:
                        con_instance = DatabaseList.objects.filter(
                            connection_name=i).first()
                        if con_instance:
                            con_name.append(
                                {
                                    'id': con_instance.id,
                                    'connection_name': con_instance.connection_name,
                                    'ip': con_instance.ip,
                                    'computer_room': con_instance.computer_room
                                })
                info = Account.objects.filter(group='admin').all()
                serializers = UserINFO(info, many=True)
                return Response(
                    {
                        'connection': con_name,
                        'person': serializers.data,
                        'assigend': assigned['person'],
                        'custom': custom_com['con_room'],
                        'multi': custom_com['multi']
                    }
                )
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == "basename":
            try:
                con_id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                _connection = DatabaseList.objects.filter(id=con_id).first()
                try:
                    with con_database.SQLgo(
                            ip=_connection.ip,
                            user=_connection.username,
                            password=_connection.password,
                            port=_connection.port
                    ) as f:
                        res = f.baseItems(sql='show databases')
                        exclude_db = serachsql.exclued_db_list()
                        for db in exclude_db:
                            if db in res:
                                res.remove(db)
                        return Response(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'tablename':
            try:
                data = json.loads(request.data['data'])
                basename = data['basename']
                con_id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                _connection = DatabaseList.objects.filter(id=con_id).first()
                try:
                    with con_database.SQLgo(
                            ip=_connection.ip,
                            user=_connection.username,
                            password=_connection.password,
                            port=_connection.port,
                            db=basename
                    ) as f:
                        res = f.baseItems(sql='show tables')
                        return Response(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'field':
            try:
                connection_info = json.loads(request.data['connection_info'])
                table = connection_info['tablename']
                basename = connection_info['basename']
                con_id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    _connection = DatabaseList.objects.filter(
                        id=con_id).first()
                    with con_database.SQLgo(
                            ip=_connection.ip,
                            user=_connection.username,
                            password=_connection.password,
                            port=_connection.port,
                            db=basename
                    ) as f:
                        field = f.gen_alter(table_name=table)
                        idx = f.index(table_name=table)
                        return Response({'idx': idx, 'field': field})

                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)


class exAES(baseview.AnyLogin):

    def get(self, request, args: str = None):

        user = request.GET.get('user')
        pwd = request.GET.get('pwd')
        if user == "admin":
            AES = cryptoAES(settings.SECRET_KEY)
            permissions = authenticate(username=user, password=pwd)
            if permissions is not None and permissions.is_active:
                all = DatabaseList.objects.all()
                for i in all:
                    DatabaseList.objects.filter(id=i.id).update(password=AES.encrypt(i.password))
                return Response('密码已加密!')
            else:
                return Response('密码错误!')
        else:
            return Response('超级管理员鉴权失败！')
