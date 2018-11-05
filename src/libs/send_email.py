from libs import util
from email.header import Header
from email.mime.text import MIMEText
from email.utils import parseaddr, formataddr
import smtplib
import ast
import logging

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class send_email(object):

    def __init__(self, to_addr=None):
        self.to_addr = to_addr
        un_init = util.init_conf()
        self.email = ast.literal_eval(un_init['message'])
        self.email_suffix_list = ast.literal_eval(util.init_conf().get('other', '')).get('email_suffix_list',[])  # 获取可以注册邮箱后缀

    def _format_addr(self, s):
        name, addr = parseaddr(s)
        return formataddr((Header(name, 'utf-8').encode(), addr))

    def send_mail(self, mail_data=None, type=None):
        if type == 0:  # 执行
            text = '<html><body><h1>Yearning 工单执行通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>地址: <a href="%s">%s</a></p>' \
                   '<br><p>工单备注: %s</p>' \
                   '<br><p>状态: 已执行</p>' \
                   '<br><p>备注: %s</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'],
                       mail_data['addr'],
                       mail_data['addr'],
                       mail_data['text'],
                       mail_data['note'])
        elif type == 1:  # 驳回
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
        elif type == 2:  ##权限申请
            text = '<html><body><h1>Yearning 权限申请通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>状态: 申请</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'])
        elif type == 3:  ## 权限同意
            text = '<html><body><h1>Yearning 权限同意通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>状态: 同意</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'])
        elif type == 4:  ##权限驳回
            text = '<html><body><h1>Yearning 权限驳回通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>状态: 驳回</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'])
        elif type == 5:  ##查询申请
            text = '<html><body><h1>Yearning 查询申请通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>状态: 提交</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'])
        elif type == 6:  ##查询同意
            text = '<html><body><h1>Yearning 查询同意通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>状态: 同意</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'])
        elif type == 7:  ##查询驳回
            text = '<html><body><h1>Yearning 查询驳回通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>状态: 驳回</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'])
        elif type == 9:
            text = '<html><body><h1>Yearning 工单转移通知</h1>' \
                   '<br><p>工单号: %s</p>' \
                   '<br><p>发起人: %s</p>' \
                   '<br><p>地址: <a href="%s">%s</a></p>' \
                   '<br><p>状态: 提交至执行人</p>' \
                   '</body></html>' % (
                       mail_data['workid'],
                       mail_data['to_user'],
                       mail_data['addr'],
                       mail_data['addr'])
        else:  # 提交
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
        msg['From'] = self._format_addr('Yearning_Admin <%s>' % self.email['user'])
        msg['Subject'] = Header('Yearning 工单消息推送', 'utf-8').encode()
        if self.email['ssl']:
            server = smtplib.SMTP_SSL(self.email['smtp_host'], self.email['smtp_port'])
        else:
            server = smtplib.SMTP(self.email['smtp_host'], self.email['smtp_port'])
        server.set_debuglevel(1)
        server.login(self.email['user'], self.email['password'])
        server.sendmail(self.email['user'], [self.to_addr], msg.as_string())
        server.quit()

    def email_check(self):
        try:
            if self.to_addr.split('@')[1] not in self.email_suffix_list:
                CUSTOM_ERROR.warning("邮箱地址[%s]不在允许注册邮箱范围内%s,请更换邮箱地址进行注册" % (self.to_addr, self.email_suffix_list))
                return 300, "邮箱地址[%s]不在允许注册邮箱范围内%s,请更换邮箱地址进行注册" % (self.to_addr, self.email_suffix_list)
            else:
                return 200, ''
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return 500, "邮箱检查失败"
