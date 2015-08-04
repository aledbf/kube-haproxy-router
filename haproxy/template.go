/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package haproxy

// Derived from the standard debian config
const header = `
global
  log /dev/log    local0
  log /dev/log    local1 notice
  maxconn 100000
  chroot /var/lib/haproxy
  stats socket /run/haproxy.sock mode 660 level admin
  stats timeout 30s
  daemon
  # Default SSL material locations
  ca-base /etc/ssl/certs
  crt-base /etc/ssl/private
  # Default ciphers to use on SSL-enabled listening sockets.
  # For more information, see ciphers(1SSL). This list is from:
  #  https://hynek.me/articles/hardening-your-web-servers-ssl-ciphers/
  ssl-default-bind-ciphers ECDH+AESGCM:DH+AESGCM:ECDH+AES256:DH+AES256:ECDH+AES128:DH+AES:ECDH+3DES:DH+3DES:RSA+AESGCM:RSA+AES:RSA+3DES:!aNULL:!MD5:!DSS
  ssl-default-bind-options no-sslv3

defaults
  log     global
  mode    http
  balance leastconn
  option  redispatch
  option  httplog
  option  dontlognull
  option  http-server-close
  option  forwardfor except 127.0.0.0/8
  option  httpchk HEAD /health-check HTTP/1.1
  timeout connect   5s
  timeout client    30s
  timeout server    30s
  timeout tunnel    1h
  timeout http-keep-alive 60s
  retries 3
  # default error files
  errorfile 403 /haproxy-errors/html/403.http
  errorfile 408 /haproxy-errors/html/408.http
  errorfile 502 /haproxy-errors/html/502.http
  errorfile 503 /haproxy-errors/html/503.http
  errorfile 504 /haproxy-errors/html/504.http

userlist users
  user stats insecure-password statspassword

listen stats :1936
  stats uri /
  stats enable

frontend health_check
  bind :12345
  monitor-uri /healthz
`
