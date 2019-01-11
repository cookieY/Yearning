import logging,json
from libs import baseview, util
from core.models import SqlOrder
from django.http import HttpResponse
from rest_framework.response import Response

CUSTOM_ERROR = logging.getLogger('Yearning.core.views')


class order(baseview.BaseView):
    '''

    :argument 我的工单展示接口api

    '''

    def get(self, request, args: str = None):
        try:
            page = request.GET.get('page')
            qurey = json.loads(request.GET.get('query'))
        except KeyError as e:
            CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
        else:
            try:
                start = (int(page) - 1) * 20
                end = int(page) * 20
                if qurey['valve']:
                    if len(qurey['picker']) == 0:
                        info = SqlOrder.objects.filter(username=request.user, text__contains=qurey['text']).order_by(
                            '-id')[start:end]

                        page_number = SqlOrder.objects.filter(username=request.user,
                                                              text__contains=qurey['text']).count()
                    else:
                        picker = []
                        for i in qurey['picker']:
                            picker.append(i)
                        info = SqlOrder.objects.filter(username=request.user, text__contains=qurey['text'],
                                                       date__gte=picker[0], date__lte=picker[1]).order_by('-id')[
                               start:end]

                        page_number = SqlOrder.objects.filter(username=request.user,
                                                              text__contains=qurey['text']).count()
                else:
                    info = SqlOrder.objects.filter(username=request.user).order_by('-id')[start:end]
                    page_number = SqlOrder.objects.filter(username=request.user).count()

                data = util.ser(info)
                return Response({'page': page_number, 'data': data})
            except Exception as e:
                CUSTOM_ERROR.error(f'{e.__class__.__name__}: {e}')
                return HttpResponse(status=500)
