'''
user permissions controller

All interfaces inherit from baseview.BaseView class

The user is_staff attribute is equal to 0
'''
import json
import logging
from django.contrib.auth import authenticate
from django.db.models import Count
from django.http import HttpResponse
from rest_framework.response import Response
from libs import gen_ddl
from libs import call_inception
from libs import baseview
from libs import con_database
from libs import util
from libs import rollback
from core import adminview
from core.models import (
    SqlOrder,
    DatabaseList,
    Account,
    Usermessage,
    SqlDictionary,
    SqlRecord,
    Todolist
)
from core.serializers import (
    MessagesUser,
    SQLGeneratDic,
    Record,
    UserINFO,
    Area
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

class OtherUser(baseview.BaseView):
    '''

    普通用户修改密码

    '''

    def post(self, request, args=None):
        if args == 'changepwd':
            try:
                username = request.data['username']
                old_password = request.data['old']
                new_password = request.data['new']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    user = authenticate(username=username, password=old_password)
                    if user is not None and user.is_active:
                        user.set_password(new_password)
                        user.save()
                        return Response('%s--密码修改成功!' % username)
                    else:
                        return Response('%s--原密码不正确请重新输入' % username)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)


class Addressing_Api(baseview.BaseView):
    '''

    get: 分页获取我的工单数据

    put: 分别返回 连接名 库名 表名 字段名 索引名

    '''

    def get(self, request, args=None):
        try:
            username = request.GET.get('user')
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                pagenumber = SqlOrder.objects.filter(
                    username=username).aggregate(alter_number=Count('id'))
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = SqlOrder.objects.raw(
                    "select core_sqlorder.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_sqlorder INNER JOIN \
                    core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                    WHERE core_sqlorder.username = '%s'ORDER BY core_sqlorder.id DESC "
                    %username)[start:end]
                data = util.ser(info)
                return Response({'page': pagenumber, 'data': data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def put(self, request, args=None):

        if args == 'connection':
            try:
                info = DatabaseList.objects.all()
                _serializers = Area(info, many=True)
                return Response(_serializers.data)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == "basename":
            try:
                id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                _connection = DatabaseList.objects.filter(id=id).first()
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
                id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                _connection = DatabaseList.objects.filter(id=id).first()
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
                id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    _connection = DatabaseList.objects.filter(id=id).first()
                    with con_database.SQLgo(
                        ip=_connection.ip,
                        user=_connection.username,
                        password=_connection.password,
                        port=_connection.port,
                        db=basename
                    ) as f:
                        res = f.showtable(table_name=table)
                        return Response(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'indexdata':
            try:
                login = json.loads(request.data['login'])
                table = request.data['table']
                basename = login['basename']
                id = request.data['id']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    _connection = DatabaseList.objects.filter(id=id).first()
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


class GenerationOrder_Api(baseview.BaseView):
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
                data = request.data['data']
                data = json.loads(data)
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


class AuthModel(baseview.BaseView):
    '''

    认证组权限

    '''

    def post(self, request, args=None):
        try:
            user = request.data['user']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                info = Account.objects.filter(username=user).get()
                return Response(info.group)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


class SQLSyntax(baseview.BaseView):
    '''

    put   美化sql  测试sql

    post 提交工单

    '''

    def put(self, request, args=None):
        if args == 'beautify':
            try:
                data = request.data['data']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    res = call_inception.Inception.BeautifySQL(sql=data)
                    return HttpResponse(res)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'test':
            try:
                id = request.data['id']
                base = request.data['base']
                sql = request.data['sql']
                sql = str(sql).strip('\n').rstrip(';')
                data = DatabaseList.objects.filter(id=id).first()
                info = {
                    'host': data.ip,
                    'user': data.username,
                    'password': data.password,
                    'db': base
                    }
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            else:
                try:
                    with call_inception.Inception(LoginDic=info) as test:
                        res = test.Check(sql=sql)
                        return Response({'result': res, 'status': 200})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response({'status': '500'})

    def post(self, request, args=None):
        try:
            data = json.loads(request.data['data'])
            tmp = json.loads(request.data['sql'])
            user = request.data['user']
            type = request.data['type']
            id = request.data['id']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                x = [x.rstrip(';') for x in tmp]
                sql = ';'.join(x)
                sql = sql.strip(' ').rstrip(';')
                workId = util.workId()
                SqlOrder.objects.get_or_create(
                    username=user,
                    date=util.date(),
                    work_id=workId,
                    status=2,
                    basename=data['basename'],
                    sql=sql,
                    type=type,
                    text=data['text'],
                    backup=data['backup'],
                    bundle_id=id
                    )
                content = DatabaseList.objects.filter(id=id).first()
                if content.url:
                    util.dingding(content='工单提交通知\n' + content.before, url=content.url)
                return Response('已提交，请等待管理员审核！')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

class MainData(baseview.BaseView):
    '''

    get  主页图表信息

    put todo列表 删除todo 个人信息

    post todo提交

    '''

    def get(self, request, args=None):
        if args == 'pie':
            try:
                alter = SqlOrder.objects.filter(type=0).aggregate(alter_number=Count('id'))
                sql = SqlOrder.objects.filter(type=1).aggregate(sql_number=Count('id'))
                data = [alter, sql]
                return Response(data)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == 'infocard':
            try:
                dic = SqlDictionary.objects.aggregate(dic_number=Count('id'))
                user = Account.objects.aggregate(user=Count('id'))
                order = SqlOrder.objects.aggregate(order=Count('id'))
                link = DatabaseList.objects.aggregate(link=Count('id'))
                data = [dic, user, order, link]
                return Response(data)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == 'messages':
            try:
                user = request.GET.get('username')
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    count = Usermessage.objects.filter(
                        state='unread',
                        to_user=user
                        ).aggregate(messagecount=Count('id'))
                    return Response(count)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

    def put(self, request, args=None):

        if args == 'todolist':
            try:
                user = request.data['username']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    todo = Todolist.objects.filter(username=user).all()
                    data = [{'title': i.content} for i in todo]
                    return Response(data)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'deltodo':
            try:
                user = request.data['username']
                todo = request.data['todo']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    Todolist.objects.filter(username=user, content=todo).delete()
                    return Response('')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'ownspace':
            user = request.data['user']
            info = Account.objects.filter(username=user).get()
            _serializers = UserINFO(info)
            return Response(_serializers.data)

    def post(self, request, args=None):
        try:
            user = request.data['username']
            todo = request.data['todo']
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                Todolist.objects.get_or_create(username=user, content=todo)
                return Response('')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


class messages(baseview.BaseView):
    '''

    get  站内信列表

    put  站内信详细内容

    post 更新站内信状态

    del 删除站内信

    '''

    def get(self, request, args=None):
        try:
            unread = Usermessage.objects.filter(
                state='unread',
                to_user=args
                ).all().order_by('-time')
            serializers_unread = MessagesUser(unread, many=True)
            read = Usermessage.objects.filter(
                state='read',
                to_user=args
                ).all().order_by('-time')
            serializers_read = MessagesUser(read, many=True)
            recovery = Usermessage.objects.filter(
                state='recovery',
                to_user=args
                ).all().order_by('-time')
            serializers_recovery = MessagesUser(recovery, many=True)
            data = {'unread': serializers_unread.data, 'read': serializers_read.data,
                    'recovery': serializers_recovery.data}
            return Response(data)
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)

    def put(self, request, args=None):
        try:
            title = request.data['title']
            time = request.data['time']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                data = Usermessage.objects.filter(to_user=args, title=title, time=time).get()
                return Response({'content': data.content, 'from_user': data.from_user})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def post(self, request, args=None):
        try:
            title = request.data['title']
            time = request.data['time']
            state = request.data['state']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                Usermessage.objects.filter(
                    to_user=str(args).rstrip('/'),
                    title=title,
                    time=time
                    ).update(state=state)
                return Response('')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def delete(self, request, args=None):
        try:
            data = str(args).split('_')
            Usermessage.objects.filter(
                to_user=data[0],
                title=data[1],
                time=data[2]
                ).update(state='recovery')
            return Response('')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)

# 数据库字典
class SqlDic(baseview.BaseView):
    def put(self, request, args=None):

        if args == 'info':
            try:
                basename = request.data['basename']
                name = request.data['name']
                TableInfoPage = int(request.data['hello'])
                TableList = int(request.data['tablelist'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    DictionaryInfo = SqlDictionary.objects.filter(
                        BaseName=basename,
                        Name=name
                        ).values('TableName')
                    DictionaryInfo.query.group_by = ['TableName']  # 不重复表名
                    all = []
                    for i in DictionaryInfo:
                        tmp = SqlDictionary.objects.filter(
                            TableName=i['TableName'],
                            BaseName=basename
                            ).all()
                        _serializers = SQLGeneratDic(tmp, many=True)
                        all.append(_serializers.data)
                    dic = []
                    for i in DictionaryInfo[TableInfoPage * 3 - 3:TableInfoPage * 3]:
                        tmp = SqlDictionary.objects.filter(
                            TableName=i['TableName'],
                            BaseName=basename
                            ).all()
                        _serializers = SQLGeneratDic(tmp, many=True)
                        dic.append(_serializers.data)
                    tablecomment = []
                    for i in DictionaryInfo[TableList * 10 - 10:TableList * 10]:
                        tmp = SqlDictionary.objects.filter(
                            TableName=i['TableName'],
                            BaseName=basename,
                            Name=name
                            ).values('TableComment')
                        tmp.query.group_by = ['TableComment']
                        tablecomment.append({'table': i, 'comment': tmp})
                    return Response({
                        'dic': dic,
                        'tablelist': tablecomment,
                        'tablepage': len(DictionaryInfo),
                        'all': all
                        })
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'tablelist':
            try:
                basename = request.data['basename']
                name = request.data['name']
                TableList = int(request.data['tablelist'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    DictionaryInfo = SqlDictionary.objects.filter(
                        BaseName=basename,
                        Name=name
                        ).values('TableName')
                    DictionaryInfo.query.group_by = ['TableName']  # 不重复表名
                    tablecomment = []
                    for i in DictionaryInfo[TableList * 10 - 10:TableList * 10]:
                        tmp = SqlDictionary.objects.filter(
                            TableName=i['TableName'],
                            BaseName=basename,
                            Name=name
                            ).values('TableComment')
                        tmp.query.group_by = ['TableComment']
                        tablecomment.append({'table': i, 'comment': tmp})
                    return Response(tablecomment)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'single':
            try:
                basename = request.data['basename']
                name = request.data['name']
                tablename = request.data['tablename']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    tmp = SqlDictionary.objects.filter(
                        BaseName=basename,
                        Name=name,
                        TableName=tablename
                        ).all()
                    _serializers = SQLGeneratDic(tmp, many=True)
                    return Response([_serializers.data])
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'datalist':
            try:
                basename = request.data['basename']
                name = request.data['name']
                signal = request.data['hello']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    tmp = adminview.Admin_dic.GenerateTableData(
                        basename=basename,
                        name=name,
                        signal=signal
                        )
                    return Response(tmp)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'getdiclist':
            try:
                name = request.data['name']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    data = SqlDictionary.objects.filter(Name=name).values('BaseName')
                    data.query.distinct = ['BaseName']
                    return Response(data)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

    def get(self, request, args=None):
        try:
            data = SqlDictionary.objects.all().values('Name')
            data.query.distinct = ['Name']
            return Response(data)
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)

    def post(self, request, args=None):
        try:
            name = request.data['name']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                data = SqlDictionary.objects.filter(Name=name).all().values('BaseName')
                data.query.distinct = ['BaseName']
                return Response(data)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=e)


class Orderdetail(baseview.BaseView):

    '''

    执行工单的详细信息

    '''

    def get(self, request, args: str = None):
        try:
            workid = request.GET.get('workid')
            status = request.GET.get('status')
            id = request.GET.get('id')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            type_id = SqlOrder.objects.filter(id=id).first()
            try:
                if status == '1':
                    data = SqlRecord.objects.filter(workid=workid).all()
                    _serializers = Record(data, many=True)
                    return Response({'data':_serializers.data, 'type':type_id.type})
                else:
                    data = SqlOrder.objects.filter(work_id=workid).first()
                    _in = {'data':[{'sql': x} for x in data.sql.split(';')], 'type':type_id.type}
                    return Response(_in)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__} : {e}')
                return HttpResponse(status=500)

    def put(self, request, args: str = None):
        try:
            id = request.data['id']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                info = SqlOrder.objects.raw(
                    "select core_sqlorder.*,core_databaselist.connection_name,\
                    core_databaselist.computer_room from core_sqlorder INNER JOIN \
                    core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                    WHERE core_sqlorder.id = %s"
                    %id)
                data = util.ser(info)
                sql = data[0]['sql'].split(';')
                _tmp = ''
                for i in sql:
                    _tmp += i + ";\n"
                return Response({'data':data[0], 'sql':_tmp.strip('\n'), 'type': 0})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def post(self, request, args: str = None):
        try:
            id = request.data['id']
            info = json.loads(request.data['opid'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                sql = []
                for i in info:
                    info = SqlOrder.objects.raw(
                        "select core_sqlorder.*,core_databaselist.connection_name,\
                        core_databaselist.computer_room from core_sqlorder INNER JOIN \
                        core_databaselist on core_sqlorder.bundle_id = core_databaselist.id \
                        WHERE core_sqlorder.id = %s"
                        % id)
                    data = util.ser(info)
                    _data = SqlRecord.objects.filter(sequence=i).first()
                    roll = rollback.rollbackSQL(db=_data.backup_dbname, opid=i)
                    link = _data.backup_dbname + '.' + roll
                    spa = rollback.roll(backdb=link, opid=i)
                    sql.append(spa)
                sql = sorted(sql)
                return Response({'data': data[0], 'sql': sql, 'type': 1})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
