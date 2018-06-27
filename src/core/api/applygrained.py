import logging
import json
import threading
import ast
from libs import baseview, send_email, util
from django.http import HttpResponse
from rest_framework.response import Response
from core.models import Account, applygrained, grained,globalpermissions

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class audit_grained(baseview.SuperUserpermissions):

    def get(self, request, args: str = None):

        user_id = Account.objects.filter(username=request.user).first().id
        page = request.GET.get('page')
        if user_id == 1:
            pn = applygrained.objects.count()
            start = int(page) * 10 - 10
            end = int(page) * 10
            user_list = applygrained.objects.all().order_by('-id')[start:end]
            ser = []
            for i in user_list:
                ser.append({'work_id': i.work_id, 'status': i.status, 'username': i.username, 'permissions': i.permissions})
            return Response({'data': ser, 'pn': pn})

        else:
            return Response([])

    def post(self, request, args: str = None):

        user = request.data['user']
        work_id = request.data['work_id']
        if request.data['status'] == 0:
            try:
                grained_list = json.loads(request.data['grained_list'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                grained.objects.filter(username=user).update(permissions=grained_list)
                applygrained.objects.filter(work_id=work_id).update(status=1)
                mail = Account.objects.filter(username=user).first()
                thread = threading.Thread(target=push_message, args=({'to_user': user, 'workid': work_id}, 3, user, mail.email, work_id, '同意'))
                thread.start()
                return Response('权限已更新成功!')
        else:
            applygrained.objects.filter(work_id=work_id).update(status=0)
            mail = Account.objects.filter(username=user).first()
            thread = threading.Thread(target=push_message, args=({'to_user': user, 'workid': work_id}, 4, user, mail.email, work_id, '驳回'))
            thread.start()
            return Response('权限已驳回!')

    def put(self, request, args: str = None):

        work_id_list = json.loads(request.data['work_id'])
        for i in work_id_list:
            applygrained.objects.filter(work_id=i).delete()
        return Response('申请记录已删除!')


class apply_grained(baseview.BaseView):

    def post(self, request, args: str = None):

        grained_list = json.loads(request.data['grained_list'])
        work_id = util.workId()
        applygrained.objects.get_or_create(work_id=work_id, username=request.user, permissions=grained_list, status=2)
        mail = Account.objects.filter(id=1).first()
        try:
            thread = threading.Thread(target=push_message, args=({'to_user': request.user, 'workid': work_id}, 2, request.user, mail.email, work_id, '已提交'))
            thread.start()
        except:
            pass
        return Response('权限申请已提交!')


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
                util.dingding(content='权限申请通知\n工单编号:%s\n发起人:%s\n状态:%s' % (work_id, user, status), url=webhook['webhook'])
        except ValueError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
