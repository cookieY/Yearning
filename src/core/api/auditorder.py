import logging
import json
from libs import send_email
from libs import baseview
from libs import util
from libs import call_inception
from libs import rollback
from rest_framework.response import Response
from django.db.models import Count
from django.http import HttpResponse
from core.models import (
    SqlOrder,
    Usermessage,
    DatabaseList,
    SqlRecord,
    Account,
    globalpermissions
)
from libs.serializers import (
    Record
)

conf = util.conf_path()
addr_ip = conf.ipaddress
CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class audit(baseview.SuperUserpermissions):
    '''
    SQL审核相关
    '''

    def get(self, request, args=None):
        try:
            page = request.GET.get('page')
            username = request.GET.get('username')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlOrder.objects.filter(assigned=username).aggregate(alter_number=Count('id'))
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = SqlOrder.objects.raw(
                    '''
                    select core_sqlorder.*,core_databaselist.connection_name, \
                    core_databaselist.computer_room from core_sqlorder \
                    INNER JOIN core_databaselist on \
                    core_sqlorder.bundle_id = core_databaselist.id where core_sqlorder.assigned = '%s'\
                    ORDER BY core_sqlorder.id desc
                    '''%username
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
                        mail = Account.objects.filter(username=to_user).first()
                        tag = globalpermissions.objects.filter(authorization='global').first()
                        ret_info = '操作成功，该请求已驳回！'
                        if tag is None or tag.dingding == 0:
                            pass
                        else:
                            try:
                                if content.url:
                                    util.dingding(
                                        content='工单驳回通知\n工单编号:%s\n发起人:%s\n地址:%s\n驳回说明:%s\n状态:驳回'
                                        %(_tmpData['work_id'],to_user,addr_ip,text), url=content.url)
                            except:
                                ret_info = '工单执行成功!但是钉钉推送失败,请查看错误日志排查错误.'
                        if tag is None or tag.email == 0:
                            pass
                        else:
                            try:
                                if mail.email:
                                    mess_info = {
                                        'workid':_tmpData['work_id'],
                                        'to_user':to_user,
                                        'addr': addr_ip,
                                        'rejected': text}
                                    put_mess = send_email.send_email(to_addr=mail.email)
                                    put_mess.send_mail(mail_data=mess_info,type=1)
                            except:
                                ret_info = '工单执行成功!但是邮箱推送失败,请查看错误日志排查错误.'
                        return Response(ret_info)
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
                        SqlOrder.objects.filter(id=id).update(status=3)
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
                                'port': SQL_LIST.port
                            }
                        ) as f:
                            res = f.Execute(sql=c.sql, backup=c.backup)
                            '''

                            修改该工单编号的state状态

                            '''
                            SqlOrder.objects.filter(id=id).update(status=1)
                            
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
                                    reviewer=c.assigned,
                                    affectrow=i['affected_rows'],
                                    sequence=i['sequence'],
                                    backup_dbname=i['backup_dbname']
                                )
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
                        mail = Account.objects.filter(username=to_user).first()
                        tag = globalpermissions.objects.filter(authorization='global').first()
                        ret_info = '操作成功，该请求已同意!并且已在相应库执行！详细执行信息请前往执行记录页面查看！'

                        if tag is None or tag.dingding == 0:
                            pass
                        else:
                            try:
                                if content.url:
                                    util.dingding(
                                        content='工单执行通知\n工单编号:%s\n发起人:%s\n地址:%s\n工单备注:%s\n状态:同意\n备注:%s'
                                                          %(c.work_id,c.username,addr_ip,c.text,content.after), url=content.url)
                            except:
                                ret_info = '工单执行成功!但是钉钉推送失败,请查看错误日志排查错误.'

                        if tag is None or tag.email == 0:
                            pass
                        else:
                            try:
                                if mail.email:
                                    mess_info = {
                                        'workid':c.work_id,
                                        'to_user':c.username,
                                        'addr': addr_ip,
                                        'text':c.text,
                                        'note': content.after}
                                    put_mess = send_email.send_email(to_addr=mail.email)
                                    put_mess.send_mail(mail_data=mess_info,type=0)
                            except:
                                ret_info = '工单执行成功!但是邮箱推送失败,请查看错误日志排查错误.'
                        return Response(ret_info)
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
                        'db': base,
                        'port': data.port
                    }
                    try:
                        with call_inception.Inception(LoginDic=info) as test:
                            res = test.Check(sql=sql.sql)
                            return Response({'result': res, 'status': 200})
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return Response({'status': '500'})

    def post(self, request, args: str = None):
        try:
            dataid = json.loads(request.data['id'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                for i in dataid:
                    if i['status'] == 1:
                        workid = SqlOrder.objects.filter(id=i['id']).first()
                        SqlRecord.objects.filter(workid=workid.work_id).delete()
                        SqlOrder.objects.filter(id=i['id']).delete()
                    else:
                        SqlOrder.objects.filter(id=i['id']).delete()
                return Response('工单数据删除成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


class orderdetail(baseview.BaseView):

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
                _h=[]
                for i in sql[0]:
                    _h.append(i[0])
                _h = sorted(_h)
                return Response({'data': data[0], 'sql': _h, 'type': 1})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
