FROM alpine:3.12 AS builder

LABEL maintainer="chaiyd <chaiyd.cn@gmail.com>"

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk update  \
    && echo 'hosts: files mdns4_minimal [NOTFOUND=return] dns mdns4' >> /etc/nsswitch.conf

ARG YEARNING_VER=2.3.5

ARG YEARNING_URL=https://github.com/cookieY/Yearning/releases/download/${YEARNING_VER}/Yearning-${YEARNING_VER}-linux-amd64.zip
RUN wget -cO yearning.zip $YEARNING_URL && \
    unzip yearning.zip -d /opt


FROM alpine:3.12

LABEL maintainer="chaiyd <chaiyd.cn@gmail.com>"

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk update  \
    && apk add --no-cache ca-certificates bash tree tzdata libc6-compat dumb-init \
    && cp -rf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone \
    && echo 'hosts: files mdns4_minimal [NOTFOUND=return] dns mdns4' >> /etc/nsswitch.conf

COPY --from=builder /opt/Yearning /opt/Yearning
#COPY --from=builder /opt/Yearning-go/dist /opt/Yearning-go/dist
COPY --from=builder /opt/conf.toml /opt/conf.toml

WORKDIR /opt/

EXPOSE 8000

ENTRYPOINT ["/usr/bin/dumb-init", "--"]
CMD ["/opt/Yearning", "run"]
