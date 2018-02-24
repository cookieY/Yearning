from __future__ import absolute_import, unicode_literals
from .models import (
    Usermessage,
    DatabaseList,
    Account,
    globalpermissions,
    SqlOrder,
    SqlRecord,
    SqlDictionary,
    grained
)
from django.http import HttpResponse
from libs import util
from libs import send_email
from libs import testddl,call_inception
import logging
import functools
CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


def grained_permissions(func):
    @functools.wraps(func)
    def wrapper(self, request, args=None):
        if request.method == "PUT" and args != 'connection':
            return func(self, request, args)
        else:
            if request.method == "GET":
                permissions_type = request.GET.get('permissions_type')
            else:
                permissions_type = request.data['permissions_type']
            user = grained.objects.filter(username=request.user).first()
            if user is not None and user.permissions[permissions_type] == '1':
                return func(self, request, args)
            else:
                return HttpResponse(status=401)
    return wrapper







import threading


class order_push_message(threading.Thread):

    def __init__(self, addr_ip, id, from_user, to_user):
        super().__init__()
        self.id = id
        self.addr_ip = addr_ip
        self.order = SqlOrder.objects.filter(id=id).first()
        self.from_user = from_user
        self.to_user = to_user

    def run(self):
        self.execute()
        self.Agreed()

    def execute(self):

        self.title = f'工单:{self.order.work_id}审核通过通知'

        '''

        根据工单编号拿出对应sql的拆解数据

        '''

        SQL_LIST = DatabaseList.objects.filter(id=self.order.bundle_id).first()
        '''

        发送sql语句到inception中执行

        '''
        with call_inception.Inception(
                LoginDic={
                    'host': SQL_LIST.ip,
                    'user': SQL_LIST.username,
                    'password': SQL_LIST.password,
                    'db': self.order.basename,
                    'port': SQL_LIST.port
                }
        ) as f:
            res = f.Execute(sql=self.order.sql, backup=self.order.backup)
            '''

            修改该工单编号的state状态

            '''
            SqlOrder.objects.filter(id=self.id).update(status=1)
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
                    base=self.order.basename,
                    workid=self.order.work_id,
                    person=self.order.username,
                    reviewer=self.order.assigned,
                    affectrow=i['affected_rows'],
                    sequence=i['sequence'],
                    backup_dbname=i['backup_dbname']
                )

                if self.order.type == 0 and \
                        i['errlevel'] == 0 and \
                        i['sql'].find('use') == -1 and \
                        i['stagestatus'] != 'Audit completed':
                    data = testddl.AutomaticallyDDL(sql=" ".join(i['sql'].split()))
                    if data['mode'] == 'pass':
                        pass
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
                    elif data['mode'] == 'drop':
                        SqlDictionary.objects.filter(
                            BaseName=self.order.basename,
                            TableName=data['TableName']
                        ).delete()

    def Agreed(self):

        '''

         站内信通知

        '''
        Usermessage.objects.get_or_create(
            from_user=self.from_user, time=util.date(),
            title=self.title, content='该工单已审核通过!', to_user=self.to_user,
            state='unread'
        )

        '''

        Dingding & Email

        '''

        content = DatabaseList.objects.filter(id=self.order.bundle_id).first()
        mail = Account.objects.filter(username=self.to_user).first()
        tag = globalpermissions.objects.filter(authorization='global').first()

        if tag is None or tag.dingding == 0:
            pass
        else:
            try:
                if content.url:
                    util.dingding(
                        content='工单执行通知\n工单编号:%s\n发起人:%s\n地址:%s\n工单备注:%s\n状态:同意\n备注:%s'
                                % (self.order.work_id, self.order.username, self.addr_ip, self.order.text, content.after), url=content.url)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}--钉钉推送失败: {e}')

        if tag is None or tag.email == 0:
            pass
        else:
            try:
                if mail.email:
                    mess_info = {
                        'workid': self.order.work_id,
                        'to_user': self.order.username,
                        'addr': self.addr_ip,
                        'text': self.order.text,
                        'note': content.after}
                    put_mess = send_email.send_email(to_addr=mail.email)
                    put_mess.send_mail(mail_data=mess_info, type=0)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}--邮箱推送失败: {e}')


class rejected_push_messages(threading.Thread):

    def __init__(self, _tmpData, to_user, addr_ip, text):
        super().__init__()
        self.to_user = to_user
        self._tmpData = _tmpData
        self.addr_ip = addr_ip
        self.text = text

    def run(self):
        self.push()

    def push(self):
        content = DatabaseList.objects.filter(id=self._tmpData['bundle_id']).first()
        mail = Account.objects.filter(username=self.to_user).first()
        tag = globalpermissions.objects.filter(authorization='global').first()
        if tag is None or tag.dingding == 0:
            pass
        else:
            try:
                if content.url:
                    util.dingding(
                        content='工单驳回通知\n工单编号:%s\n发起人:%s\n地址:%s\n驳回说明:%s\n状态:驳回'
                                % (self._tmpData['work_id'], self.to_user, self.addr_ip, self.text), url=content.url)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}--钉钉推送失败: {e}')
        if tag is None or tag.email == 0:
            pass
        else:
            try:
                if mail.email:
                    mess_info = {
                        'workid': self._tmpData['work_id'],
                        'to_user': self.to_user,
                        'addr': self.addr_ip,
                        'rejected': self.text}
                    put_mess = send_email.send_email(to_addr=mail.email)
                    put_mess.send_mail(mail_data=mess_info, type=1)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}--邮箱推送失败: {e}')


class submit_push_messages(threading.Thread):

    def __init__(self, workId, user, addr_ip, text, assigned, id):
        super().__init__()
        self.workId = workId
        self.user = user
        self.addr_ip = addr_ip
        self.text = text
        self.assigned = assigned
        self.id = id

    def run(self):
        self.submit()

    def submit(self):
        content = DatabaseList.objects.filter(id=self.id).first()
        mail = Account.objects.filter(username=self.assigned).first()
        tag = globalpermissions.objects.filter(authorization='global').first()
        if tag is None or tag.dingding == 0:
            pass
        else:
            if content.url:
                try:
                    util.dingding(
                        content='工单提交通知\n工单编号:%s\n发起人:%s\n地址:%s\n工单说明:%s\n状态:已提交\n备注:%s'
                                % (self.workId, self.user, self.addr_ip, self.text, content.before), url=content.url)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}--钉钉推送失败: {e}')
        if tag is None or tag.email == 0:
            pass
        else:
            if mail.email:
                mess_info = {
                    'workid': self.workId,
                    'to_user': self.user,
                    'addr': self.addr_ip,
                    'text': self.text,
                    'note': content.before}
                try:
                    put_mess = send_email.send_email(to_addr=mail.email)
                    put_mess.send_mail(mail_data=mess_info, type=2)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}--邮箱推送失败: {e}')