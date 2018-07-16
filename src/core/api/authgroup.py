# -*- coding: utf-8 -*-
# __author__ = "mysqlplus@163.com"
# Date: 2018/7/10
import logging
import json
from django.http import HttpResponse
from libs import baseview
from rest_framework.response import Response
from core.models import Account, Auth_Group, grained
from libs.serializers import AuthGroup_Serializers

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class auth_group(baseview.BaseView):

    def get(self, request, args: str = None):
        if args == 'all':
            try:
                page = request.GET.get('page')
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    page_number = Auth_Group.objects.count()
                    start = int(page) * 10 - 10
                    end = int(page) * 10
                    queryset = Auth_Group.objects.all()[start:end]
                    serializers = AuthGroup_Serializers(queryset, many=True)
                    return Response({'page': page_number, 'data': serializers.data})
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(e)
        elif args == 'permissions':
            try:
                group_name = request.GET.get('group_name')
                group = Auth_Group.objects.filter(group_name=group_name).first()
                return Response(group.permissions)
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)

        elif args == 'group_name':
            try:
                obj = Auth_Group.objects.values('group_name')
                group_list = []
                for i in obj:
                    group_list.append(i['group_name'])
                return Response({'authgroup': group_list})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)

    def post(self, request, args: str = None):
        try:
            group_name = request.data['groupname']
            permissions = json.loads(request.data['permission'])
            Auth_Group.objects.get_or_create(group_name=group_name, permissions=permissions)
            return Response('权限组已创建!')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(e)

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
                    'dic': '0',
                    'diccon': [],
                    'dicedit': '0',
                    'user': '0',
                    'base': '0',
                    'dicexport': '0',
                    'person': [],
                    'query': '0',
                    'querycon': []
                }
                for group_name in group_list:
                    auth = Auth_Group.objects.filter(group_name=group_name).values('permissions')
                    auth = auth[0]['permissions']
                    for k, v in perm.items():
                        if isinstance(v, list):
                            v = list(set(v) | set(auth[k]))
                        elif v == '0':
                            v = auth[k]
                        perm[k] = v
                return Response({'permissions': perm})
        elif args == 'save_info':
            try:
                username = request.data['username']
                group = request.data['group']
                department = request.data['department']
                authgroup = request.data['auth_group']
                group_dict = json.loads(request.data['permission_list'])
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)
            else:
                auth_group_str = (",".join(authgroup))
                Account.objects.filter(username=username).update(group=group,
                                                                 department=department, auth_group=auth_group_str)
                grained.objects.filter(username=username).update(permissions=group_dict)
                return Response('权限保存成功!')
        else:
            try:
                group_name = request.data['groupname']
                permissions = json.loads(request.data['permission'])
                Auth_Group.objects.filter(group_name=group_name).update(permissions=permissions)
                return Response('权限组更新成功!')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(e)
