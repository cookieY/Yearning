'''
admin permissions controller

All interfaces inherit from baseview.SuperUserpermissions class

Access is only available when the user is_staff attribute is equal to 1
'''
import configparser
import logging
import json
from django.contrib.auth import authenticate
from rest_framework.response import Response
from django.db.models import Count
from libs import call_inception
from libs import baseview
from libs import con_database
from libs import testddl
from libs import exportdocx
from libs import util
from django.http import (
    HttpResponse,
    StreamingHttpResponse
)
from core.serializers import (
    UserINFO,
    SQLGeneratDic,
    Sqllist,
    Getdingding
)
from core.models import (
    Account,
    SqlDictionary,
    SqlOrder,
    DatabaseList,
    SqlRecord,
    Usermessage,
    Todolist
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')



class Userinfo_Api(baseview.SuperUserpermissions):
    '''
        User Management interface

        mothod：

        get:

            get all user information, a page consists of 20 user info

        put:

            if args equal to changepwd (/api/v1/userinfo/changepwd) change the password

            if args equal to changegroup (/api/v1/userinfo/changegroup) change the group

        post: 
   
            add user

        delete:
   
            del user
      
    '''
    def get(self, request, args=None):
        if args == 'all':
            try:
                page = request.GET.get('page')
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    pagenumber = Account.objects.aggregate(alter_number=Count('id'))
                    start = int(page) * 10 - 10
                    end = int(page) * 10
                    info = Account.objects.all()[start:end]
                    serializers = UserINFO(info, many=True)
                    return Response({'page': pagenumber, 'data': serializers.data})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(e)

    def put(self, request, args=None):
        if args == 'changepwd':
            try:
                username = request.data['username']
                old_password = request.data['old']
                new_password = request.data['new']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    user = authenticate(username=username, password=old_password)
                    if user is not None and user.is_active:
                        user.set_password(new_password)
                        user.save()
                    return Response('%s--密码修改成功!' % username)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
        elif args == 'changegroup':
            try:
                username = request.data['username']
                group = request.data['group']
                department = request.data['department']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    if group == 'admin':
                        Account.objects.filter(username=username).update(
                            group=group,
                            department=department,
                            is_staff=1
                            )
                        return Response('%s--用户组修改成功!' % username)
                    elif group == 'guest':
                        Account.objects.filter(username=username).update(
                            group=group,
                            department=department, 
                            is_staff=0
                            )
                        return Response('%s--用户组修改成功!' % username)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

    def post(self, request, args=None):
        try:
            username = request.data['username']
            password = request.data['password']
            group = request.data['group']
            department = request.data['department']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                if group == 'admin':
                    user = Account.objects.create_user(
                        username=username,
                        password=password,
                        department=department,
                        group=group,
                        is_staff=1)
                    user.save()
                    return Response('%s 用户注册成功!' % username)
                elif group == 'guest':
                    user = Account.objects.create_user(
                        username=username,
                        password=password,
                        department=department,
                        group=group
                        )
                    user.save()
                    return Response('%s 用户注册成功!' % username)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def delete(self, request, args=None):
        try:
            Account.objects.filter(username=args).delete()
            Usermessage.objects.filter(to_user=args).delete()
            Todolist.objects.filter(username=args).delete()
            return Response('%s--用户已删除!' % args)
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)



class ManagementSql_Api(baseview.SuperUserpermissions):
    '''
    数据库管理相关
    '''
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
                return Response({'page': pagenumber, 'data': serializers.data, 'diclist': data})
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
            DatabaseList.objects.filter(connection_name=args).delete()
            return Response('数据库信息已删除!')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)



class AuditSql_Api(baseview.SuperUserpermissions):
    '''
    SQL审核相关
    '''
    def get(self, request, args=None):
        try:
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlOrder.objects.aggregate(alter_number=Count('id'))
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = SqlOrder.objects.raw(
                    '''
                    select core_sqlorder.*,core_databaselist.connection_name, \
                    core_databaselist.computer_room from core_sqlorder \
                    INNER JOIN core_databaselist on \
                    core_sqlorder.bundle_id = core_databaselist.id \
                    ORDER BY core_sqlorder.id desc
                    '''
                )[start:end]
                data = util.ser(info)
                return Response({'page': pagenumber, 'data': data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def put(self, request, args=None):
        try:
            type = request.data['type']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            if type == 0:
                try:
                    from_user = request.data['from_user']
                    to_user = request.data['to_user']
                    text = request.data['text']
                    id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    try:
                        SqlOrder.objects.filter(id=id).update(status=0)
                        _tmpData = SqlOrder.objects.filter(id=id).values(
                            'work_id',
                            'bundle_id'
                            ).first()
                        title = '工单:' + _tmpData['work_id'] + '驳回通知'
                        Usermessage.objects.get_or_create(
                            from_user=from_user,
                            time=util.date(),
                            title=title,
                            content=text,
                            to_user=to_user,
                            state='unread'
                        )
                        content = DatabaseList.objects.filter(id=_tmpData['bundle_id']).first()
                        if content.url:
                            util.dingding(content='工单驳回通知\n' + text, url=content.url)
                        return Response('操作成功，该请求已驳回！')
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return HttpResponse(status=500)

            elif type == 1:
                try:
                    from_user = request.data['from_user']
                    to_user = request.data['to_user']
                    id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    try:
                        c = SqlOrder.objects.filter(id=id).first()
                        title = f'工单:{c.work_id}审核通过通知'

                        '''
                        
                        根据工单编号拿出对应sql的拆解数据
                        
                        '''

                        SQL_LIST = DatabaseList.objects.filter(id=c.bundle_id).first()
                        '''
                        
                        发送sql语句到inception中执行
                        
                        '''
                        with call_inception.Inception(
                            LoginDic={
                                'host': SQL_LIST.ip,
                                'user': SQL_LIST.username,
                                'password': SQL_LIST.password,
                                'db': c.basename,
                                'port':SQL_LIST.port
                                }
                            ) as f:
                            res = f.Execute(sql=c.sql, backup=c.backup)

                            '''
                            
                            遍历返回结果插入到执行记录表中
                            
                            '''
                            for i in res:
                                SqlRecord.objects.get_or_create(
                                    date=util.date(),
                                    state=i['stagestatus'],
                                    sql=i['sql'],
                                    area=SQL_LIST.computer_room,
                                    name=SQL_LIST.connection_name,
                                    error=i['errormessage'],
                                    base=c.basename,
                                    workid=c.work_id,
                                    person=c.username,
                                    reviewer=from_user,
                                    affectrow=i['affected_rows'],
                                    sequence=i['sequence'],
                                    backup_dbname=i['backup_dbname']
                                )

                                if c.type == 0 and \
                                                i['errlevel'] == 0 and \
                                                i['sql'].find('use') == -1 and \
                                                i['stagestatus'] != 'Audit completed':
                                    data = testddl.AutomaticallyDDL(sql=i['sql'])
                                    if data['mode'] == 'edit':
                                        SqlDictionary.objects.filter(
                                            BaseName=data['BaseName'],
                                            TableName=data['TableName'],
                                            Field=data['Field']
                                            ).update(
                                                Type=data['Type'],
                                                Null=data['Null'],
                                                Default=data['Default'])

                                    elif data['mode'] == 'add':
                                        SqlDictionary.objects.get_or_create(
                                            Type=data['Type'],
                                            Null=data['Null'],
                                            Default=data['Default'],
                                            Extra=data['COMMENT'],
                                            BaseName=data['BaseName'],
                                            TableName=data['TableName'],
                                            Field=data['Field'],
                                            TableComment='',
                                            Name=SQL_LIST.connection_name
                                        )

                                    elif data['mode'] == 'del':
                                        SqlDictionary.objects.filter(
                                            BaseName=data['BaseName'],
                                            TableName=data['TableName'],
                                            Field=data['Field'],
                                            Name=SQL_LIST.connection_name).delete()

                        '''
                        
                        修改该工单编号的state状态
                        
                        '''
                        SqlOrder.objects.filter(id=id).update(status=1)

                        '''
                        
                        通知消息
                        
                        '''
                        Usermessage.objects.get_or_create(
                            from_user=from_user, time=util.date(),
                            title=title, content='该工单已审核通过!', to_user=to_user,
                            state='unread'
                        )

                        '''
                        
                        Dingding
                        
                        '''

                        content = DatabaseList.objects.filter(id=c.bundle_id).first()
                        if content.url:
                            util.dingding(content='工单执行通知\n' + content.after, url=content.url)
                        return Response('操作成功，该请求已同意!并且已在相应库执行！详细执行信息请前往执行记录页面查看！')
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return HttpResponse(status=500)

            elif type == 'test':
                try:
                    base = request.data['base']
                    id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    sql = SqlOrder.objects.filter(id=id).first()
                    data = DatabaseList.objects.filter(id=sql.bundle_id).first()
                    info = {
                        'host': data.ip,
                        'user': data.username,
                        'password': data.password,
                        'db': base
                        }
                    try:
                        with call_inception.Inception(LoginDic=info) as test:
                            res = test.Check(sql=sql.sql)
                            return Response({'result': res, 'status': 200})
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return Response({'status': '500'})



class RecordC(baseview.SuperUserpermissions):

    '''
    审核记录相关
    '''

    def get(self, request, args=None):
        try:
            info = []
            page = request.GET.get('page')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlRecord.objects.all().values('workid')
                pagenumber.query.distinct = ['workid']
                start = int(page) * 10 - 10
                end = int(page) * 10
                workid = SqlRecord.objects.all().values('workid')[start:end]
                workid.query.distinct = ['workid']
                for i in workid:
                    dataset = SqlRecord.objects.filter(workid=i['workid']).all()
                    buld_id = SqlOrder.objects.filter(work_id=i['workid']).first()
                    info.append({'workid': dataset[0].workid,
                                 'date': dataset[0].date,
                                 'person': dataset[0].person,
                                 'area': dataset[0].area,
                                 'base': dataset[0].base,
                                 'name': dataset[0].name,
                                 'reviewer': dataset[0].reviewer,
                                 'id': buld_id.id
                                })
                return Response({'data': info, 'page': len(pagenumber)})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


class ExportDoc(baseview.SuperUserpermissions):
    '''
    导出数据字典为docx文档
    '''
    def post(self, request, args=None):
        try:
            conf = configparser.ConfigParser()
            conf.read('deploy.conf')
            ip = conf.get('mysql', 'address')
            user = conf.get('mysql', 'username')
            db = conf.get('mysql', 'db')
            password = conf.get('mysql', 'password')
        except Exception:
            CUSTOM_ERROR.error('''The configuration file information is missing!''')
            return HttpResponse(status=500)
        else:
            try:
                data = json.loads(request.data['data'])
                connection_name = request.data['connection_name']
                basename = request.data['basename']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    c = exportdocx.ToWord(
                        Host=ip,
                        User=user,
                        Password=password,
                        Database=db,
                        Charset='utf8')
                    a = c.exportTables(Conn=connection_name, Schemal=basename, TableList=data)
                    return Response(
                        {
                            'status': 'docx文档已生成',
                            'url': '%s_%s_Dictionary_%s.docx' % (connection_name, basename, a)
                        }
                    )
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)


class DingDing(baseview.SuperUserpermissions):
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
            id = request.data['id']
            before = request.data['before']
            after = request.data['after']
            url = request.data['url']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                DatabaseList.objects.filter(id=id).update(before=before, after=after, url=url)
                return Response('ok')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

class Admin_dic(baseview.SuperUserpermissions):
    '''
    数据库字典相关 admin权限
    '''
    @staticmethod
    def DicGenerate(id, basename):
        '''
        字典生成
        '''
        _connection = DatabaseList.objects.filter(id=id).first()
        with con_database.SQLgo(
            ip=_connection.ip,
            user=_connection.username,
            password=_connection.password,
            db=basename,
            port=_connection.port
            ) as f:
            res = f.tablename()
            for i in res:
                EveryData = f.showtable(table_name=i)
                for c in EveryData:
                    if c['Default'] is not None:
                        SqlDictionary.objects.get_or_create(
                            Field=c['Field'],
                            Type=c['Type'],
                            Null=c['Null'],
                            Default=c['Default'],
                            Extra=c['Extra'],
                            BaseName=basename,
                            TableName=i,
                            TableComment=c['TableComment'],
                            Name=_connection.connection_name
                            )
                    else:
                        SqlDictionary.objects.get_or_create(
                            Field=c['Field'],
                            Type=c['Type'],
                            Null=c['Null'],
                            Extra=c['Extra'],
                            BaseName=basename,
                            TableName=i,
                            TableComment=c['TableComment'],
                            Name=_connection.connection_name
                            )

    @staticmethod
    def GenerateTableData(basename=None, name=None, signal=None):
        '''
        生成表结构数据
        '''
        signal = int(signal)
        DictionaryInfo = SqlDictionary.objects.filter(
            BaseName=basename,
            Name=name
            ).values('TableName')
        DictionaryInfo.query.group_by = ['TableName']  # 不重复表名
        dic = []
        if signal == 1 or signal is None:
            for i in DictionaryInfo[:signal * 3]:
                tmp = SqlDictionary.objects.filter(
                    TableName=i['TableName'],
                    BaseName=basename
                    ).all()
                serializers = SQLGeneratDic(tmp, many=True)
                dic.append(serializers.data)
        else:
            for i in DictionaryInfo[signal * 3 - 3:signal * 3]:
                tmp = SqlDictionary.objects.filter(
                    TableName=i['TableName'],
                    BaseName=basename
                    ).all()
                serializers = SQLGeneratDic(tmp, many=True)
                dic.append(serializers.data)
        return dic

    def put(self, request, args: str = None):
        
        if args == 'Generation':  # 一次性自动扫描数据库表结构并且把信息插入sqldic表
            try:
                id = request.data['id']
                basename = json.loads(request.data['basename'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    for i in basename:
                        Admin_dic.DicGenerate(id, i)
                    return HttpResponse('ok')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'deldic':
            try:
                Name = request.data['name']
                BaseName = request.data['basename']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    for i in BaseName:
                        SqlDictionary.objects.filter(Name=Name, BaseName=i).delete()
                    return Response('字典已删除！')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'edittableinfo':
            try:
                basename = request.data['basename']
                tablename = request.data['tablename']
                name = request.data['name']
                signal = request.data['hello']
                comment = request.data['comment']
                singleid = request.data['singleid']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    if singleid == '0':
                        SqlDictionary.objects.filter(
                            BaseName=basename,
                            TableName=tablename
                            ).update(TableComment=comment)
                        tmp = Admin_dic.GenerateTableData(
                            basename=basename, 
                            name=name, 
                            signal=signal
                            )
                        return Response(tmp)
                    else:
                        SqlDictionary.objects.filter(
                            BaseName=basename,
                            TableName=tablename
                            ).update(TableComment=comment)
                        tmp = SqlDictionary.objects.filter(
                            BaseName=basename,
                            Name=name,
                            TableName=tablename
                            ).all()
                        serializers = SQLGeneratDic(tmp, many=True)
                        return Response([serializers.data])
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response('%s 表备注更新失败，请联系cookie' % tablename)

        elif args == 'editfelid':
            try:
                basename = request.data['basename']
                tablename = request.data['tablename']
                comment = request.data['comment']
                felid = request.data['felid']
                name = request.data['name']
                signal = request.data['hello']
                singleid = request.data['singleid']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    if singleid == '0':
                        SqlDictionary.objects.filter(
                            BaseName=basename,
                            TableName=tablename,
                            Field=felid
                            ).update(Extra=comment)
                        tmp = Admin_dic.GenerateTableData(
                            basename=basename,
                            name=name,
                            signal=signal
                            )
                        return Response(tmp)
                    else:
                        SqlDictionary.objects.filter(
                            BaseName=basename,
                            TableName=tablename,
                            Field=felid
                            ).update(Extra=comment)
                        tmp = SqlDictionary.objects.filter(
                            BaseName=basename,
                            Name=name,
                            TableName=tablename
                            ).all()
                        serializers = SQLGeneratDic(tmp, many=True)
                        return Response([serializers.data])
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response('%s 表备注更新失败，请联系cookie' % felid)

        elif args == 'deltable':
            try:
                basename = request.data['basename']
                tablename = request.data['tablename']
                ConnectionName = request.data['ConnectionName']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                SqlDictionary.objects.filter(
                    BaseName=basename,
                    TableName=tablename,
                    Name=ConnectionName
                    ).delete()
                return Response('ok')


def downloadFile(req):
    '''
    导出docx 文档下载接口
    '''
    filename = 'exportData/' + req.GET['url']

    def file_iterator(file_name, chunk_size=512):
        '''
        分块下载
        '''
        with open(file_name, 'rb') as f:
            while True:
                c = f.read(chunk_size)
                if c:
                    yield c
                else:
                    break

    response = StreamingHttpResponse(file_iterator(filename))
    response['Content-Type'] = 'application/octet-stream'
    response['Content-Disposition'] = f'attachment;filename="{filename}"'
    return response
