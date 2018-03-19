import json
import logging
from django.http import HttpResponse
from rest_framework.response import Response
from libs import gen_ddl, baseview, con_database
from core.task import grained_permissions
from core.models import (
    DatabaseList,
    Account,
    grained,
    SqlDictionary
)
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
                if request.data['permissions_type'] == 'user':
                    info = DatabaseList.objects.all()
                    con_name = Area(info, many=True).data
                    dic = SqlDictionary.objects.all().values('Name')
                    dic.query.distinct = ['Name']
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
                info = Account.objects.filter(is_staff=1).all()
                serializers = UserINFO(info, many=True)
                assigned = grained.objects.filter(username=request.user).first()
                return Response({'connection': con_name, 'person': serializers.data, 'dic': dic, 'assigend': assigned.permissions['person']})
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


class gensql(baseview.BaseView):
    '''
    put 生成DDL语句 生成索引语句

    '''

    def put(self, request, args=None):

        if args == "sql":
            try:
                data = request.data['data']
                data = json.loads(data)
                base = request.data['basename']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                info1 = []
                try:
                    for i in data:
                        if 'edit' in i.keys():
                            info = gen_ddl.create_sql(select_name='edit',
                                                      column_name=i['edit']['Field'],
                                                      column_type=i['edit']['Type'],
                                                      default=i['edit']['Default'],
                                                      comment=i['edit']['Extra'],
                                                      null=i['edit']['Null'],
                                                      table_name=i['table_name'],
                                                      base_name=base)
                            info1.append(info)

                        elif 'del' in i.keys():
                            info = gen_ddl.create_sql(select_name='del',
                                                      column_name=i['del']['Field'],
                                                      table_name=i['table_name'],
                                                      base_name=base)
                            info1.append(info)
                        elif 'add' in i.keys() and i['add'] != []:
                            for n in i['add']:
                                info = gen_ddl.create_sql(select_name='add',
                                                          column_name=n['Field'],
                                                          base_name=base,
                                                          column_type=n['Type'],
                                                          default=n['Default'],
                                                          comment=n['Extra'],
                                                          null=n['Null'],
                                                          table_name=i['table_name'])

                                info1.append(info)
                    return Response(info1)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == "index":
            try:
                data = json.loads(request.data['data'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                info1 = []
                try:
                    for i in data:
                        if 'delindex' in i.keys():
                            info = gen_ddl.index(select_name='delindex',
                                                 key_name=i['delindex']['key_name'],
                                                 table_name=i['table_name'])
                            info1.append(info)
                        elif 'addindex' in i.keys() and i['addindex'] != []:
                            for n in i['addindex']:
                                if n['fulltext'] == "YES":
                                    info = gen_ddl.index(table_name=i['table_name'],
                                                         column_name=n['column_name'],
                                                         key_name=n['key_name'],
                                                         fulltext=n['fulltext'],
                                                         select_name='addindex')
                                    info1.append(info)
                                elif n['Non_unique'] == "YES":
                                    info = gen_ddl.index(select_name='addindex',
                                                         key_name=n['key_name'],
                                                         non_unique='unique',
                                                         column_name=n['column_name'],
                                                         table_name=i['table_name'])
                                    info1.append(info)
                                else:
                                    info = gen_ddl.index(select_name='addindex',
                                                         key_name=n['key_name'],
                                                         column_name=n['column_name'],
                                                         table_name=i['table_name'])
                                    info1.append(info)
                    return Response(info1)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)