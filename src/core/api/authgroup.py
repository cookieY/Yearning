# __author__ = "mysqlplus@163.com"
# __review__ = "cookie"
# Date: 2018/7/10
import logging
import json
from django.http import HttpResponse
from libs import baseview
from rest_framework.response import Response
from core.models import Account, grained
from core.task import isAdmin

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class auth_group(baseview.BaseView):

    @isAdmin
    def get(self, request, args: str = None):
        if args == 'all':
            try:
                page = request.GET.get('page')
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    page_number = grained.objects.count()
                    start = int(page) * 10 - 10
                    end = int(page) * 10
                    queryset = grained.objects.order_by('-id').all()[start:end]
                    ser = []
                    for i in queryset:
                        ser.append(
                            {'id': i.id, 'username': i.username, 'permissions': i.permissions})
                    return Response({'page': page_number, 'data': ser})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(e)

        elif args == 'group_name':
            try:
                obj = grained.objects.values('username')
                group_list = [x['username'] for x in obj]
                return Response({'authgroup': group_list})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)

    @isAdmin
    def post(self, request, args: str = None):
        try:
            group_name = request.data['groupname']
            permissions = json.loads(request.data['permission'])
            grained.objects.get_or_create(
                username=group_name, permissions=permissions)
            return Response('权限组已创建!')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(e)

    @isAdmin
    def put(self, request, args: str = None):
        if args == 'group_list':
            try:
                group_str = request.data['group_list']
                group_list = json.loads(group_str)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)
            else:
                perm = {
                    'ddl': '0',
                    'ddlcon': [],
                    'dml': '0',
                    'dmlcon': [],
                    'user': '0',
                    'base': '0',
                    'person': [],
                    'query': '0',
                    'querycon': []
                }
                for group_name in group_list:
                    auth = grained.objects.filter(username=group_name).first()
                    if auth is not None:
                        for k, v in perm.items():
                            if isinstance(v, list):
                                v = list(set(v) | set(auth.permissions[k]))
                            elif v == '0':
                                v = auth.permissions[k]
                            perm[k] = v
                return Response({'permissions': perm})

        elif args == 'save_info':
            try:
                username = request.data['username']
                group = request.data['group']
                department = request.data['department']
                authgroup = request.data['auth_group']
                pr = 1
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)
            else:
                try:
                    # 当用户从admin 角色组改变为guest/perform时，需要删除所有权限组下，上级审核人与之匹配的值
                    u = Account.objects.filter(username=username).first()
                    if u.group != 'guest' and group != 'admin':
                        per = grained.objects.all().values('username', 'permissions')
                        for i in per:
                            for c in i['permissions']:
                                if isinstance(i['permissions'][c], list) and c == 'person':
                                    i['permissions'][c] = list(filter(lambda x: x != username, i['permissions'][c]))
                            grained.objects.filter(username=i['username']).update(permissions=i['permissions'])

                    if group == "guest":
                        pr = 0
                    if not authgroup:
                        Account.objects.filter(username=username).update(
                            group=group, department=department, auth_group=None, is_staff=pr
                        )
                    else:
                        auth_group_str = (",".join(authgroup))
                        Account.objects.filter(username=username).update(
                            group=group, department=department, auth_group=auth_group_str, is_staff=pr)
                    return Response('权限保存成功!')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(e)

        elif args == 'update':
            try:
                group_name = request.data['groupname']
                permissions = json.loads(request.data['permission'])
                select = ['query', 'ddl', 'dml']
                for i in select:
                    if permissions[i] == '0':
                        index = f'{i}con'
                        permissions[index] = []
                grained.objects.filter(username=group_name).update(
                    permissions=permissions)
                return Response('权限组更新成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)

    @isAdmin
    def delete(self, request, args: str = None):
        user = Account.objects.all().values('username', 'auth_group')
        for i in user:
            if i['auth_group'] is not None:
                auth_list = i['auth_group'].split(',')
                for c in auth_list:
                    if c == args:
                        auth_list.remove(c)
                Account.objects.filter(username=i['username']).update(
                    auth_group=','.join(auth_list))
        grained.objects.filter(username=args).delete()
        return Response('权限组删除成功！')
