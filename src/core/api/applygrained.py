import logging
import json
import configparser
from libs import baseview, send_email, util
from django.http import HttpResponse
from rest_framework.response import Response
from core.models import Account, applygrained, grained
from libs.serializers import audit_grained_serializers

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

CONF = configparser.ConfigParser()
CONF.read('deploy.conf')
WEBHOOK = CONF.get('dingding', 'webhook')

class audit_grained(baseview.SuperUserpermissions):

    def get(self, request, args: str = None):

        user_id = Account.objects.filter(username=request.user).first().id

        if user_id == 0:
            user_list = applygrained.objects.all()
            serializers = audit_grained_serializers(user_list, many=True)
            return Response(serializers.data)

        else:
            return Response([])

    def post(self, request, args: str = None):

        user = request.data['user']

        if request.data['status'] == 0:
            try:
                grained_list = json.loads(request.data['grained_list'])
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                grained.objects.filter(username=user).update(permissions=grained_list)
                mail = Account.objects.filter(username=user).first().email
                put_mess = send_email.send_email(to_addr=mail)
                put_mess.send_mail(mail_data={'to_user': user}, type=3)
                return Response('权限已更新成功!')
        else:
            mail = Account.objects.filter(username=user).first().email
            put_mess = send_email.send_email(to_addr=mail)
            put_mess.send_mail(mail_data={'to_user': user}, type=4)
            return Response('权限已驳回!')


class apply_grained(baseview.BaseView):

    def post(self, request, args: str = None):

        grained_list = json.loads(request.data['grained_list'])
        work_id = util.workId()
        applygrained.objects.get_or_create(work_id=work_id, username=request.user, permissions=grained_list)
        mail = Account.objects.filter(id=0).first().email
        if mail != '':
            put_mess = send_email.send_email(to_addr=mail)
            put_mess.send_mail(mail_data={'to_user': request.user}, type=4)

        if WEBHOOK != '':
            util.dingding(content='权限申请通知\n工单编号:%s\n发起人:%s\n状态:已执行' % (work_id, request.user))
        return Response('权限申请已提交!')

