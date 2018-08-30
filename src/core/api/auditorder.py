import logging
import json
import ast
import threading
from libs import baseview, call_inception, util, serializers, send_email
from rest_framework.response import Response
from django.http import HttpResponse
from core.models import (
    SqlOrder,
    DatabaseList,
    SqlRecord,
    Account,
    globalpermissions
)

from core.task import order_push_message, rejected_push_messages

conf = util.conf_path()
addr_ip = conf.ipaddress
CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class audit(baseview.SuperUserpermissions):
    '''

    :argument 审核页面相关操作api接口

    '''

    def get(self, request, args: str = None):

        '''

        :argument 审核页面数据展示请求接口

        :param None

        :return 数据条数, 数据

        '''

        try:
            page = request.GET.get('page')
            username = request.GET.get('username')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                un_init = util.init_conf()
                custom_com = ast.literal_eval(un_init['other'])
                page_number = SqlOrder.objects.filter(assigned=username).count()
                start = (int(page) - 1) * 20
                end = int(page) * 20
                info = SqlOrder.objects.raw(
                    '''
                    select core_sqlorder.*,core_databaselist.connection_name, \
                    core_databaselist.computer_room from core_sqlorder \
                    INNER JOIN core_databaselist on \
                    core_sqlorder.bundle_id = core_databaselist.id where core_sqlorder.assigned = '%s'\
                    ORDER BY core_sqlorder.id desc
                    ''' % username
                )[start:end]
                data = util.ser(info)
                info = Account.objects.filter(group='perform').all()
                ser = serializers.UserINFO(info, many=True)
                return Response(
                    {'page': page_number, 'data': data, 'multi': custom_com['multi'], 'multi_list': ser.data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

    def put(self, request, args: str = None):

        '''

        :argument 工单确认执行,驳回,二次检测接口。

        :param category 根据获得的category值执行具体的操作逻辑

        :return 提交结果信息

        '''

        try:
            category = request.data['type']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            if category == 0:
                try:
                    to_user = request.data['to_user']
                    text = request.data['text']
                    order_id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    try:
                        SqlOrder.objects.filter(id=order_id).update(status=0,rejected=text)
                        _tmpData = SqlOrder.objects.filter(id=order_id).values(
                            'work_id',
                            'bundle_id'
                        ).first()
                        rejected_push_messages(_tmpData, to_user, addr_ip, text).start()
                        return Response('操作成功，该请求已驳回！')
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return HttpResponse(status=500)

            elif category == 1:
                try:
                    from_user = request.data['from_user']
                    to_user = request.data['to_user']
                    order_id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    try:
                        idempotent = SqlOrder.objects.filter(id=order_id).first()
                        if idempotent.status != 2:
                            return Response('非法传参，触发幂等操作')
                        else:
                            SqlOrder.objects.filter(id=order_id).update(status=3)
                            order_push_message(addr_ip, order_id, from_user, to_user).start()
                            return Response('工单执行成功!请通过记录页面查看具体执行结果')
                    except Exception as e:
                        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                        return HttpResponse(status=500)
            elif category == 2:
                try:
                    perform = request.data['perform']
                    work_id = request.data['work_id']
                    username = request.data['username']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    mail = Account.objects.filter(username=perform).first()
                    SqlOrder.objects.filter(work_id=work_id).update(assigned=perform)
                    threading.Thread(target=push_message, args=(
                        {'to_user': username, 'workid': work_id, 'addr': addr_ip}, 9, request.user, mail.email,
                        work_id,
                        '已提交执行人')).start()
                    return Response('工单已提交执行人！')

            elif category == 'test':
                try:
                    base = request.data['base']
                    order_id = request.data['id']
                except KeyError as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)
                else:
                    sql = SqlOrder.objects.filter(id=order_id).first()
                    if not sql.sql:
                        return Response({'status': '工单内无sql语句!'})
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
                        return Response({'status': '请检查inception信息是否正确!'})


class del_order(baseview.BaseView):
    '''

    :argument 审核页面工单删除操作请求api

    :param data_id 根据data_id['status'] 值执行相应的删除逻辑

    :return 删除结果信息

    '''

    def post(self, request, args: str = None):
        try:
            data_id = json.loads(request.data['id'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                for i in data_id:
                    if i['status'] == 1:
                        work_id = SqlOrder.objects.filter(id=i['id']).first()
                        SqlRecord.objects.filter(workid=work_id.work_id).delete()
                        SqlOrder.objects.filter(id=i['id']).delete()
                    else:
                        SqlOrder.objects.filter(id=i['id']).delete()
                return Response('工单数据删除成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)


def push_message(message=None, type=None, user=None, to_addr=None, work_id=None, status=None):
    try:
        tag = globalpermissions.objects.filter(authorization='global').first()
        if tag.message['mail']:
            put_mess = send_email.send_email(to_addr=to_addr)
            put_mess.send_mail(mail_data=message, type=type)
    except Exception as e:
        CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
    else:
        try:
            if tag.message['ding']:
                un_init = util.init_conf()
                webhook = ast.literal_eval(un_init['message'])
                util.dingding(content='工单转移通知\n工单编号:%s\n发起人:%s\n状态:%s' % (work_id, user, status),
                              url=webhook['webhook'])
        except ValueError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
