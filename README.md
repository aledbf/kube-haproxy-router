# kube-haproxy-router

Replace nginx to avoid kube-proxy to reach a pod.

`Internet -> Nginx -> kube-proxy -> iptales (ip service) -> flannel -> docker bridge -> node (listen port app in go doing lb) -> pod/s`

with

`Internet -> kube-haproxy -> pod/s`

Changes:
  - remove nginx
  - do not use service VIP to reach a pod
  - use the name of the application (hostname) to route traffic
  - use haproxy checks

This borrow ideas from #12111 and #11679 from GoogleCloudPlatform/kubernetes


```
docker run \
  --name kube-haproxy \
  --rm \
  -p 80:80 \
  -p 1936:1936 \
  -p 8081:8081 \
  aledbf/kube-haproxy-router:0.0.1 \
  /kube-haproxy \
  --master http://$(etcdctl get /deis/scheduler/k8s/master):8080 \
  --domain=$(etcdctl get /deis/platform/domain)
```
