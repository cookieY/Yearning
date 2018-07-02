import logging
from django.http import HttpResponse
from rest_framework.response import Response
from libs import baseview, call_inception, con_database

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class osc_step(baseview.SuperUserpermissions):

    def get(self, request, args: str = None):

        '''

        :argument 根据获得的sha1,返回对应sql的osc进度

        '''

        try:
            with call_inception.Inception(LoginDic={
                'host': '',
                'user': '',
                'password': '',
                'db': '',
                'port': ''
            }) as f:
                data = f.oscstep(sql="inception get osc_percent '%s';" % args)
                return Response(data)
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(e)

    def delete(self, request, args: str = None):

        '''

        :argument: 根据获得的SHA1, 终止对应sql的osc 并返回执行结果

        '''

        try:
            with call_inception.Inception(LoginDic={
                'host': '',
                'user': '',
                'password': '',
                'db': '',
                'port': ''
            }) as f:
                f.oscstep(sql=f"inception stop alter '{args}';")
                return Response('osc已终止,请刷新后查看详细信息')
        except Exception as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(e)
