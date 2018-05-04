import json
import logging
import time
import re
import threading
import configparser
from django.db.models import Count
from libs import util
from rest_framework.response import Response
from libs.serializers import Query_review, Query_list
from libs import baseview, send_email
from libs import con_database
from core.models import DatabaseList, Account, querypermissions, query_order

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')
conf = util.conf_path()
CONF = configparser.ConfigParser()
CONF.read('deploy.conf')
WEBHOOK = CONF.get('webhook', 'dingding')


class search(baseview.BaseView):

    '''
    :argument   sql查询接口, 过滤非查询语句并返回查询结果。
                可以自由limit数目 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    def post(self, request, args=None):
        sql = request.data['sql']
        check = str(sql).strip().split(';\n')
        user = query_order.objects.filter(username=request.user).order_by('-id').first()
        if user.query_per == 1:
            if check[-1].strip().lower().startswith('s') != 1:
                return Response({'error': '只支持查询功能或删除不必要的空白行！'})
            else:
                address = json.loads(request.data['address'])
                _c = DatabaseList.objects.filter(
                    connection_name=user.connection_name,
                    computer_room=user.computer_room
                ).first()
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
                            work_id=user.work_id,
                            username=request.user,
                            statements=query_sql
                        )
                        return Response(data_set)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return Response({'error': '查询失败，请查看错误日志定位具体问题'})
        else:
            return Response({'error': '已超过申请时限请刷新页面后重新提交申请'})

    def put(self, request, args: str = None):
        base = request.data['base']
        table = request.data['table']
        query_per = query_order.objects.filter(username=request.user).order_by('-id').first()
        if query_per.query_per == 1:
            _c = DatabaseList.objects.filter(
                connection_name=query_per.connection_name,
                computer_room=query_per.computer_room
                ).first()
            try:
                with con_database.SQLgo(
                        ip=_c.ip,
                        password=_c.password,
                        user=_c.username,
                        port=_c.port,
                        db=base
                ) as f:
                    data_set = f.search(sql='desc %s'%table)
                return Response(data_set)
            except:
                return Response('')
        else:
            return Response({'error': '已超过申请时限请刷新页面后重新提交申请'})


def replace_limit(sql, limit):

    '''

    :argument 根据正则匹配分析输入信息 当limit数目超过配置文件规定的最大数目时将会采用配置文件的最大数目

    '''

    if sql[-1] != ';':
        sql += ';'
    if sql.startswith('show') == -1:
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
        if int(length) <= int(limit):
            return sql
        else:
            sql = re.sub(r'limit\s.*\d.*;', 'limit %s;' % limit, sql)
            return sql
    else:
        sql = sql.rstrip(';') + ' limit %s;'%limit
        return sql



class query_worklf(baseview.BaseView):

    @staticmethod
    def query_callback(timer, work_id):
        query_order.objects.filter(work_id=work_id).update(query_per=1)
        try:
            time.sleep(int(timer) * 60)
        except:
            time.sleep(60)
        finally:
            query_order.objects.filter(work_id=work_id).update(query_per=3)

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

    def put(self, request, args: str = None):

        if request.data['mode'] == 'put':
            instructions = request.data['instructions']
            connection_name = request.data['connection_name']
            computer_room = request.data['computer_room']
            timer = request.data['timer']
            export = request.data['export']
            audit = request.data['audit']
            work_id = util.workId()
            try:
                timer = int(timer)
                query_order.objects.create(
                    work_id=work_id,
                    instructions=instructions,
                    username=request.user,
                    timer=timer,
                    date=util.date(),
                    query_per=2,
                    connection_name=connection_name,
                    computer_room=computer_room,
                    export= export,
                    audit=audit,
                    time=util.date()
                )
            except:
                query_order.objects.create(
                    work_id=work_id,
                    instructions=instructions,
                    username=request.user,
                    timer=1,
                    date=util.date(),
                    query_per=2,
                    connection_name=connection_name,
                    computer_room=computer_room,
                    export=export,
                    audit=audit,
                    time=util.date()
                )
            userinfo = Account.objects.filter(username=audit, group='admin').first()
            thread = threading.Thread(target=push_message, args=({'to_user': request.user, 'workid': work_id}, 5, request.user, userinfo.email, work_id, '提交'))
            thread.start()
            ## 钉钉及email站内信推送
            return Response('查询工单已提交，等待管理员审核！')

        elif request.data['mode'] == 'agree':
            work_id = request.data['work_id']
            query_info = query_order.objects.filter(work_id=work_id).order_by('-id').first()
            t = threading.Thread(target=query_worklf.query_callback, args=(query_info.timer, work_id))
            userinfo = Account.objects.filter(username=query_info.username).first()
            thread = threading.Thread(target=push_message, args=({'to_user': query_info.username, 'workid': query_info.work_id}, 6, query_info.username, userinfo.email, work_id, '同意'))
            t.start()
            thread.start()
            return Response('查询工单状态已更新！')

        elif request.data['mode'] == 'disagree':
            work_id = request.data['work_id']
            query_order.objects.filter(work_id=work_id).update(query_per=0)
            query_info = query_order.objects.filter(work_id=work_id).order_by('-id').first()
            userinfo = Account.objects.filter(username=query_info.username).first()
            thread = threading.Thread(target=push_message, args=({'to_user': query_info.username, 'workid': query_info.work_id}, 7, query_info.username, userinfo.email,work_id, '驳回'))
            thread.start()
            return Response('查询工单状态已更新！')

        elif request.data['mode'] == 'status':
            status = query_order.objects.filter(username=request.user).order_by('-id').first()
            try:
                return Response(status.query_per)
            except:
                return Response(0)

        elif request.data['mode'] == 'info':
            tablelist = []
            database = query_order.objects.filter(username=request.user).order_by('-id').first()
            _connection = DatabaseList.objects.filter(connection_name=database.connection_name).first()
            with con_database.SQLgo(ip=_connection.ip,
                        user=_connection.username,
                        password=_connection.password,
                        port=_connection.port) as f:
                dataname = f.query_info(sql='show databases')
            children = []
            ignore = ['information_schema', 'sys', 'performance_schema', 'mysql']
            for index,uc in enumerate(dataname):
                for cc in ignore:
                    if uc['Database'] == cc:
                        del dataname[index]
                        index = index - 1
            for i in dataname:
                with con_database.SQLgo(ip=_connection.ip,
                                        user=_connection.username,
                                        password=_connection.password,
                                        port=_connection.port,
                                        db=i['Database']) as f:
                    tablename = f.query_info(sql='show tables')
                for c in tablename:
                    key = 'Tables_in_%s'%i['Database']
                    children.append({
                        'title': c[key]
                    })
                tablelist.append({
                    'title': i['Database'],
                    'children': children
                })
                children = []
            data = [{
                'title': database.connection_name,
                'expand': 'true',
                'children': tablelist
            }]
            return Response({'info':json.dumps(data),'status': database.export})

    def delete(self, request, args: str = None):

        data = query_order.objects.filter(username=request.user).order_by('-id').first()
        query_order.objects.filter(work_id=data.work_id).delete()
        return Response('')


def push_message(message=None, type=None, user=None, to_addr=None, work_id=None, status=None):
    try:
        put_mess = send_email.send_email(to_addr=to_addr)
        put_mess.send_mail(mail_data=message, type=type)
    except Exception as e:
        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
    else:
        try:
            util.dingding(content='查询申请通知\n工单编号:%s\n发起人:%s\n状态:%s' % (work_id, user, status), url=WEBHOOK)
        except ValueError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')


class Query_order(baseview.SuperUserpermissions):

    def get(self, request, args: str = None):
        page = request.GET.get('page')
        pn = query_order.objects.filter(audit=request.user).all().values('id')
        pn.query.distinct = ['id']
        start = int(page) * 10 - 10
        end = int(page) * 10
        user_list = query_order.objects.all().order_by('-id')[start:end]
        serializers = Query_review(user_list, many=True)
        return Response({'data': serializers.data, 'pn': len(pn)})

    def post(self, request, args: str = None):

        work_id_list = json.loads(request.data['work_id'])
        for i in work_id_list:
            query_order.objects.filter(work_id=i).delete()
        return Response('申请记录已删除!')
