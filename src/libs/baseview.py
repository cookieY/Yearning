from django.views.generic.base import View
from django.utils.decorators import method_decorator
from rest_framework.decorators import api_view
from rest_framework.permissions import (
    IsAdminUser,
    AllowAny
)
from rest_framework.views import APIView

@method_decorator(api_view(['DELETE', 'GET', 'POST', 'PUT']), 'dispatch')
class BaseView(View):
    def dispatch(self, *args, **kwargs):
        return super(BaseView, self).dispatch(*args, **kwargs)

    def get(self, request, args: str = None):
        pass

    def post(self, request, args: str = None):
        pass

    def put(self, request, args: str = None):
        pass

    def delete(self, request, args: str = None):
        pass


class SuperUserpermissions(APIView):
    
    permission_classes = (IsAdminUser,)

    def get(self, request, args: str = None):
        pass

    def post(self, request, args: str = None):
        pass

    def put(self, request, args: str = None):
        pass

    def delete(self, request, args: str = None):
        pass


class AnyLogin(APIView):

    permission_classes = ()
    authentication_classes = ()

    def get(self, request, args: str = None):
        pass

    def post(self, request, args: str = None):
        pass

    def put(self, request, args: str = None):
        pass

    def delete(self, request, args: str = None):
        pass
