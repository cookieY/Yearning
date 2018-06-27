import json
import logging
import ast
from django.http import HttpResponse
from rest_framework.response import Response
from libs import baseview, con_database, util
from core.task import grained_permissions
from core.models import (
    DatabaseList,
    Account,
    grained,
    SqlDictionary
)
from libs.serializers import (
    Area,
    UserINFO,
    query_con
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
                assigned = grained.objects.filter(username=request.user).first()
                un_init = util.init_conf()
                custom_com = ast.literal_eval(un_init['other'])
                if request.data['permissions_type'] == 'user' or request.data['permissions_type'] == 'own_space':
                    info = DatabaseList.objects.all()
                    con_name = Area(info, many=True).data
                    dic = SqlDictionary.objects.all().values('Name')
                    dic.query.distinct = ['Name']

                elif request.data['permissions_type'] == 'query':
                    datalist = DatabaseList.objects.all()
                    serializers = query_con(datalist, many=True)
                    assigned = grained.objects.filter(username=request.user).first()
                    return Response({'assigend': assigned.permissions['person'],'connection': serializers.data, 'custom': custom_com['con_room']})
                else:
                    con_name = []
                    _type = request.data['permissions_type'] + 'con'
                    permission_spec = grained.objects.filter(username=request.user).first()
                    for i in permission_spec.permissions[_type]:
                        con_instance = DatabaseList.objects.filter(connection_name=i).first()
                        if con_instance:
                            con_name.append(
                                {
                                    'id': con_instance.id,
                                    'connection_name': con_instance.connection_name,
                                    'ip': con_instance.ip,
                                    'computer_room': con_instance.computer_room
                                })
                    dic = ''
                info = Account.objects.filter(group='admin').all()
                serializers = UserINFO(info, many=True)
                return Response(
                    {
                        'connection': con_name,
                        'person': serializers.data,
                        'dic': dic,
                        'assigend': assigned.permissions['person'],
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
                        res = f.basename()
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
                        res = f.tablename()
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
                    _connection = DatabaseList.objects.filter(id=con_id).first()
                    with con_database.SQLgo(
                        ip=_connection.ip,
                        user=_connection.username,
                        password=_connection.password,
                        port=_connection.port,
                        db=basename
                    ) as f:
                        res = f.gen_alter(table_name=table)
                        return Response(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'indexdata':
            try:
                login = json.loads(request.data['login'])
                table = request.data['table']
                basename = login['basename']
                con_id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    _connection = DatabaseList.objects.filter(id=con_id).first()
                    with con_database.SQLgo(
                        ip=_connection.ip,
                        user=_connection.username,
                        password=_connection.password,
                        port=_connection.port,
                        db=basename
                    ) as f:
                        res = f.index(table_name=table)
                        return Response(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response(e)


