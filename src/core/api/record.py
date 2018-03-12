import logging
from libs import baseview, util
from rest_framework.response import Response
from django.http import HttpResponse
from core.models import SqlOrder

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class recordorder(baseview.SuperUserpermissions):

    '''
    审核记录相关
    '''

    def get(self, request, args=None):
        try:
            page = request.GET.get('page')
            username = request.GET.get('username')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlOrder.objects.filter(status=1).all().values('id')
                pagenumber.query.distinct = ['id']
                start = int(page) * 10 - 10
                end = int(page) * 10
                sql = SqlOrder.objects.raw(
                    '''
                    select core_sqlorder.*,core_databaselist.connection_name, \
                    core_databaselist.computer_room from core_sqlorder \
                    INNER JOIN core_databaselist on \
                    core_sqlorder.bundle_id = core_databaselist.id where core_sqlorder.status = 1 and core_sqlorder.assigned = '%s'\
                    ORDER BY core_sqlorder.id desc
                    '''%username
                )[start:end]
                data = util.ser(sql)
                return Response({'data': data, 'page': len(pagenumber)})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)