# kube-haproxy-router

Replace nginx to avoid kube-proxy to reach a pod.

Internet -> nginx -> kube-proxy (service VIP and load balancer) -> pod

with

Internet -> kube-haproxy -> pod

Changes:
  - remove nginx
  - do not use service VIP to reach a pod
  - use the name of the application (hostname) to route traffic
  - use haproxy checks

This borrow ideas from #12111 and #11679 from GoogleCloudPlatform/kubernetes

kube-haproxy --server http://<kubernetes api master>:8080