from libs import util
from email.header import Header
from email.mime.text import MIMEText
from email.utils import parseaddr, formataddr
import smtplib

conf = util.conf_path()
from_addr = conf.mail_user
password = conf.mail_password
smtp_server = conf.smtp
smtp_port = conf.smtp_port


class send_email(object):

    def __init__(self, to_addr=None):
        self.to_addr = to_addr

    def _format_addr(self, s):
        name, addr = parseaddr(s)
        return formataddr((Header(name, 'utf-8').encode(), addr))

    def send_mail(self,mail_data=None,type=None):
        if type == 0: #执行
            text = '<html><body><h1>Yearning 工单执行通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>地址: <a href="%s">%s</a></p>' \
                   '<br><p>工单备注: %s</p>' \
                   '<br><p>状态: 已执行</p>' \
                   '<br><p>备注: %s</p>' \
                   '</body></html>' %(
                mail_data['workid'],
                mail_data['to_user'],
                mail_data['addr'],
                mail_data['addr'],
                mail_data['text'],
                mail_data['note'])
        elif type == 1: #驳回
            text = '<html><body><h1>Yearning 工单驳回通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>地址: <a href="%s">%s</a></p>' \
                   '<br><p>状态: 驳回</p>' \
                   '<br><p>驳回说明: %s</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'],
                       mail_data['addr'],
                       mail_data['addr'],
                       mail_data['rejected'])
        else: #提交
            text = '<html><body><h1>Yearning 工单提交通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>地址: <a href="%s">%s</a></p>' \
                   '<br><p>工单备注: %s</p>' \
                   '<br><p>状态: 已提交</p>' \
                   '<br><p>备注: %s</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'],
                       mail_data['addr'],
                       mail_data['addr'],
                       mail_data['text'],
                       mail_data['note'])
        msg = MIMEText(text, 'html', 'utf-8')
        msg['From'] = self._format_addr('Yearning_Admin <%s>' % from_addr)
        msg['To'] = self._format_addr('Dear_guest <%s>' % self.to_addr)
        msg['Subject'] = Header('Yearning 工单消息推送', 'utf-8').encode()

        server = smtplib.SMTP(smtp_server, int(smtp_port))
        server.set_debuglevel(1)
        server.login(from_addr, password)
        server.sendmail(from_addr, [self.to_addr], msg.as_string())
        server.quit()
