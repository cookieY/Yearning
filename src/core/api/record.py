import logging
from libs import baseview
from rest_framework.response import Response
from django.http import HttpResponse
from core.models import (
    SqlRecord,
    SqlOrder
)

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class recordorder(baseview.SuperUserpermissions):

    '''
    审核记录相关
    '''

    def get(self, request, args=None):
        try:
            info = []
            page = request.GET.get('page')
            username = request.GET.get('username')
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
            return HttpResponse(status=500)
        else:
            try:
                pagenumber = SqlRecord.objects.filter(reviewer=username).all().values('workid')
                pagenumber.query.distinct = ['workid']
                start = int(page) * 10 - 10
                end = int(page) * 10
                workid = SqlRecord.objects.filter(reviewer=username).all().values('workid')[start:end]
                workid.query.distinct = ['workid']
                for i in workid:
                    dataset = SqlRecord.objects.filter(workid=i['workid']).all()
                    buld_id = SqlOrder.objects.filter(work_id=i['workid']).first()
                    info.append({'workid': dataset[0].workid,
                                 'date': dataset[0].date,
                                 'person': dataset[0].person,
                                 'area': dataset[0].area,
                                 'base': dataset[0].base,
                                 'name': dataset[0].name,
                                 'reviewer': dataset[0].reviewer,
                                 'id': buld_id.id,
                                 'text': buld_id.text
                                })
                return Response({'data': info, 'page': len(pagenumber)})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
            