import logging
import json
from libs import baseview
from rest_framework.response import Response
from django.http import HttpResponse
from core.models import (
    SqlOrder,
    Account,
    DatabaseList,
    Todolist
)
from libs.serializers import (
    UserINFO
)
from core.task import set_auth_group

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class dashboard(baseview.BaseView):
    '''

    :argument 主页面展示数据接口api

    get  主页图表信息

    put todo列表 删除todo 个人信息

    post todo提交

    '''

    def get(self, request, args=None):
        if args == 'pie':
            try:
                alter = SqlOrder.objects.filter(
                    type=0, username=request.user).count()
                sql = SqlOrder.objects.filter(
                    type=1, username=request.user).count()
                return Response([alter, sql])
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == 'infocard':
            try:
                user = Account.objects.count()
                order = SqlOrder.objects.filter(username=request.user).count()
                link = DatabaseList.objects.count()
                return Response([user, order, link])
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == 'messages':
            try:
                statement = Account.objects.filter(
                    username=request.user).first()
                if statement.id == 1:
                    return Response({'statement': statement.last_name})
                else:
                    return Response({'statement': 'pass'})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == 'menu':

            permissions = set_auth_group(request.user)
            return Response(json.dumps(permissions))

    def put(self, request, args=None):

        if args == 'todolist':
            try:
                todo = Todolist.objects.filter(username=request.user).all()
                return Response([{'title': i.content} for i in todo])
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)

        elif args == 'deltodo':
            try:
                todo = request.data['todo']
            except KeyError as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            else:
                try:
                    Todolist.objects.filter(
                        username=request.user, content=todo).delete()
                    return Response('')
                except Exception as e:
                    CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                    return HttpResponse(status=500)

        elif args == 'ownspace':
            info = Account.objects.filter(username=request.user).get()
            _serializers = UserINFO(info)
            permissions = set_auth_group(request.user)
            return Response({'userinfo': _serializers.data, 'permissons': permissions})

        elif args == 'statement':
            Account.objects.filter(
                username=request.user).update(last_name='pass')
            return Response('')

    def post(self, request, args=None):
        try:
            todo = request.data['todo']
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                Todolist.objects.get_or_create(
                    username=request.user, content=todo)
                return Response('')
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
