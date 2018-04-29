# -*- coding: utf-8 -*-

import json
from collections import OrderedDict

from openapi_codec import OpenAPICodec
from openapi_codec.encode import generate_swagger_object
from coreapi.compat import force_bytes

from django.conf import settings

from rest_framework.views import APIView
from rest_framework.response import Response
from rest_framework.schemas import SchemaGenerator

from rest_framework_swagger.renderers import (
    SwaggerUIRenderer,
    OpenAPIRenderer
)


class SwaggerSchemaView(APIView):
    renderer_classes = [
        OpenAPIRenderer,
        SwaggerUIRenderer
    ]

    def load_swagger_json(self, doc):
        """
        加载自定义swagger.json文档
        """
        data = generate_swagger_object(doc)
        with open(settings.API_DOC_PATH) as s:
            doc_json = json.load(s, object_pairs_hook=OrderedDict)

        data['paths'].update(doc_json.pop('paths'))
        data.update(doc_json)
        return OpenAPICodec().decode(force_bytes(json.dumps(data)))

    def get(self, request):
        generator = SchemaGenerator(title='后端API文档',
                                    urlconf='chess_user.urls')
        schema = generator.get_schema(request=request)
        document = self.load_swagger_json(schema)

        return Response(document)