import logging
import json
from libs import baseview, util
from rest_framework.response import Response
from core.models import globalpermissions, Account
from django.http import HttpResponse

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class setting_view(baseview.SuperUserpermissions):

    def get(self, request, args: str = None):
        user_id = Account.objects.filter(username=request.user).first().id
        if user_id == 1:
            setting = globalpermissions.objects.filter(authorization='global').first()
            return Response(
                {
                    'inception': dict(setting.inception),
                    'ldap': dict(setting.ldap),
                    'message': dict(setting.message),
                    'other': dict(setting.other)
                }
            )
        else:
            return Response({'other': 'refused'})

    def put(self, request, args: str = None):

        try:
            if args == '1':  # ldap测试
                ldap = json.loads(request.data['ldap'])
                ldap_test = util.test_auth(
                    url=ldap['url'],
                    user=ldap['user'],
                    password=ldap['password'])
                if ldap_test:
                    return Response('ldap连接成功!')
                else:
                    return Response('ldap连接失败!')
            elif args == '2':
                ding = request.data['ding']
                util.dingding('yearning webhook测试', ding)
                return Response('已发送测试消息，请在钉钉中查看')

            else:
                mail = json.loads(request.data['mail'])
                import smtplib
                from email.utils import parseaddr, formataddr
                from email.mime.text import MIMEText
                from email.header import Header

                def _format_addr(s):
                    name, addr = parseaddr(s)
                    return formataddr((Header(name, 'utf-8').encode(), addr))
                msg = MIMEText('Yearning test Message!', 'plain', 'utf-8')
                msg['From'] = _format_addr('Yearning_Admin <%s>' % mail['user'])
                msg['Subject'] = Header('Yearning 消息推送测试', 'utf-8').encode()
                if mail['ssl']:
                    server = smtplib.SMTP_SSL(mail['smtp_host'], mail['smtp_port'])  # SMTP协议默认端口是25
                else:
                    server = smtplib.SMTP(mail['smtp_host'], mail['smtp_port'])  # SMTP协议默认端口是25
                server.set_debuglevel(1)
                server.login(mail['user'], mail['password'])
                server.sendmail(mail['user'], [mail['to_user']], msg.as_string())
                server.quit()
                return Response('已发送测试邮件，请注意查收！')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(e)

    def post(self, request, args: str = None):

        try:
            inception = json.loads(request.data['inception'])
            ldap = json.loads(request.data['ldap'])
            message = json.loads(request.data['message'])
            other = json.loads(request.data['other'])
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                globalpermissions.objects.filter(authorization='global').update(inception=inception, ldap=ldap,
                                                                                message=message, other=other)
                return Response('配置信息保存成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return Response('配置信息保存失败！请通过错误日志查看具体信息')
