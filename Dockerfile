FROM alpine:3.2

RUN apk add -U haproxy

ADD haproxy-errors /haproxy-errors

COPY kube-haproxy /kube-haproxy
