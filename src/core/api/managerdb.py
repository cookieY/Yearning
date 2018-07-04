import logging
import json
import ast
from libs import baseview
from libs import con_database
from core.task import grained_permissions
from libs import util
from rest_framework.response import Response
from django.http import HttpResponse
from django.db import transaction
from libs.serializers import Sqllist, Getdingding
from core.models import (
    DatabaseList,
    SqlDictionary,
    SqlRecord,
    SqlOrder,
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
                un_init = util.init_conf()
                custom_com = ast.literal_eval(un_init['other'])
                page_number = DatabaseList.objects.count()
                start = int(page) * 10 - 10
                end = int(page) * 10
                info = DatabaseList.objects.all()[start:end]
                serializers = Sqllist(info, many=True)
                data = SqlDictionary.objects.all().values('Name')
                data.query.group_by = ['Name']  # 不重复表名

                return Response(
                    {
                        'page': page_number,
                        'data': serializers.data,
                        'diclist': data,
                        'custom': custom_com['con_room']
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

        if args == 'test':

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

        elif args == 'update':

            try:
                update_data = json.loads(request.data['data'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    DatabaseList.objects.filter(
                        connection_name=update_data['connection_name'],
                        computer_room=update_data['computer_room']).update(
                        ip=update_data['ip'],
                        username=update_data['username'],
                        password=update_data['password'],
                        port=update_data['port']
                    )
                    return Response('数据信息更新成功！')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

    def delete(self, request, args=None):

        '''

        :argument 删除数据库连接,并删除改数据库连接相关的工单记录,执行记录，以及权限表等相关所有数据

        :return: success or fail

        '''

        try:
            connection_name = request.GET.get('del')
            with transaction.atomic():
                con_id = DatabaseList.objects.filter(connection_name=connection_name).first()
                work_id = SqlOrder.objects.filter(bundle_id=con_id.id).first()
                SqlRecord.objects.filter(workid=work_id).delete()
                SqlOrder.objects.filter(bundle_id=con_id.id).delete()
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
