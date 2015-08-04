FROM alpine:3.2

RUN apk add -U haproxy

ADD haproxy-errors /haproxy-errors

ADD haproxy/haproxy.tmpl /haproxy.tmpl

COPY kube-haproxy /kube-haproxy

EXPOSE 80 443 2222

