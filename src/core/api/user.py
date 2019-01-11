import logging
import json
from libs import baseview, util
from core.task import set_auth_group, ThinkTooMuch
from libs.serializers import UserINFO
from libs.send_email import send_email
from rest_framework.response import Response
from django.http import HttpResponse
from django.contrib.auth import authenticate
from django.db import transaction
from rest_framework_jwt.settings import api_settings
from core.models import (
    Account,
    Todolist,
    grained,
    query_order
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')

jwt_payload_handler = api_settings.JWT_PAYLOAD_HANDLER
jwt_encode_handler = api_settings.JWT_ENCODE_HANDLER


class userinfo(baseview.BaseView):
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

    @ThinkTooMuch
    def get(self, request, args=None):
        if args == 'all':
            try:
                page = request.GET.get('page')
                user = request.GET.get('username').strip()
                department = request.GET.get('department').strip()
                valve = request.GET.get('valve')
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    start = int(page) * 10 - 10
                    end = int(page) * 10
                    if valve == 'true':
                        if department != '':
                            page_number = Account.objects.filter(username__contains=user,
                                                                 department__contains=department).count()
                            user = Account.objects.filter(username__contains=user, department__contains=department)[start:end]
                        else:
                            page_number = Account.objects.filter(username__contains=user).count()
                            user = Account.objects.filter(username__contains=user)[start:end]
                    else:
                        page_number = Account.objects.count()
                        user = Account.objects.all()[start:end]
                    serializers = UserINFO(user, many=True)
                    return Response({'page': page_number, 'data': serializers.data})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(e)

        elif args == 'permissions':
            user = set_auth_group(request.GET.get('user'))
            return Response(user)

    @ThinkTooMuch
    def put(self, request, args=None):
        if args == 'changepwd':
            try:
                username = request.data['username']
                password = request.data['new']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    user = Account.objects.get(username__exact=username)
                    user.set_password(password)
                    user.save()
                    return Response('%s--密码修改成功!' % username)
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'changemail':
            try:
                username = request.data['username']
                mail = request.data['mail']
                real = request.data['real']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    _send_mail = send_email(to_addr=mail)
                    _status, _message = _send_mail.email_check()
                    if _status != 200:
                        return Response(data=_message)
                    Account.objects.filter(username=username).update(email=mail, real_name=real)
                    return Response('E-mail/真实姓名修改成功!')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

    def post(self, request, args=None):
        try:
            username = request.data['username']
            password = request.data['password']
            group = request.data.get('group', 'guest')
            email = request.data['email']
            realname = request.data['realname']
            department = request.data['department']
            auth_group = ','.join(json.loads(request.data['auth_group']))
            _send_mail = send_email(to_addr=email)
            _status, _message = _send_mail.email_check()
            if _status != 200:
                return Response(data=_message)
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                if group == 'admin' or group == 'perform':
                    user = Account.objects.create_user(
                        username=username,
                        password=password,
                        department=department,
                        group=group,
                        is_staff=1,
                        email=email,
                        auth_group=auth_group,
                        real_name=realname)
                    user.save()
                    return Response('%s 用户注册成功!' % username)
                elif group == 'guest':
                    user = Account.objects.create_user(
                        username=username,
                        password=password,
                        department=department,
                        group=group,
                        email=email,
                        auth_group=auth_group,
                        real_name=realname
                    )
                    user.save()
                    return Response('%s 用户注册成功!' % username)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)

    @ThinkTooMuch
    def delete(self, request, args=None):
        try:
            pr = Account.objects.filter(username=args).first()
            if pr.is_staff == 1:
                per = grained.objects.all().values('username', 'permissions')
                for i in per:
                    for c in i['permissions']:
                        if isinstance(i['permissions'][c], list) and c == 'person':
                            i['permissions'][c] = list(filter(lambda x: x != args, i['permissions'][c]))
                    grained.objects.filter(username=i['username']).update(permissions=i['permissions'])
            with transaction.atomic():
                query_order.objects.filter(username=args).update(query_per=3)
                Account.objects.filter(username=args).delete()
                Todolist.objects.filter(username=args).delete()
            return Response('%s--用户已删除!' % args)
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)


class ldapauth(baseview.AnyLogin):
    '''

    ldap用户认证

    '''

    def post(self, request, args: str = None):
        try:
            username = request.data['username']
            password = request.data['password']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            valite = util.auth(username=username, password=password)
            if valite:
                user = Account.objects.filter(username=username).first()
                if user is not None:
                    user.set_password(util.workId())
                    user.save()
                    payload = jwt_payload_handler(user)
                    token = jwt_encode_handler(payload)
                    return Response({'token': token, 'res': '', 'permissions': user.group})
                else:
                    permissions = Account.objects.create_user(
                        username=username,
                        password=util.workId(),
                        is_staff=0,
                        group='guest')
                    permissions.save()
                    token = jwt_encode_handler(jwt_payload_handler(permissions))
                    return Response({'token': token, 'res': '', 'permissions': 'guest'})
            else:
                return HttpResponse(status=401)


class login_register(baseview.AnyLogin):

    def post(self, request, args=None):
        try:
            userdata = json.loads(request.data['userinfo'])
            _send_mail = send_email(to_addr=userdata['email'])
            _status, _message = _send_mail.email_check()
            if _status != 200:
                return Response(data=_message)
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                user = Account.objects.create_user(
                    username=userdata['username'],
                    password=userdata['password'],
                    department=userdata['department'],
                    group='guest',
                    email=userdata['email'],
                    real_name=userdata['realname'])
                user.save()
                return Response('%s 用户注册成功!' % userdata['username'])
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse('用户名已存在，请使用其他用户名注册！')


class login_auth(baseview.AnyLogin):

    def post(self, request, args: str = None):

        '''
        普通登录类型认证
        :return: jwt token
        '''

        try:
            user = request.data['username']
            password = request.data['password']
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            permissions = authenticate(username=user, password=password)
            if permissions is not None and permissions.is_active:
                token = jwt_encode_handler(jwt_payload_handler(permissions))
                return Response(
                    {'token': token, 'res': '', 'permissions': permissions.group, 'real_name': permissions.real_name})
            else:
                return HttpResponse(status=400)
