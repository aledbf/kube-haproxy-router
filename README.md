# kube-haproxy-router

Replace nginx to avoid kube-proxy to reach a pod.

`Internet-> Nginx -> iptables (VIP) -> kube-proxy -> go LB -> flannel -> docker bridge -> pod/s`

with

`Internet -> kube-haproxy -> flannel -> docker bridge -> pod/s`

**Changes:**
  - remove nginx
  - do not use service VIP to reach a pod
  - use the name of the application (hostname) to route traffic
  - use haproxy checks

This borrow ideas from #12111 and #11679 in `github.com/GoogleCloudPlatform/kubernetes`

**Content type**

Some applications, like a REST API always return json content. In this cases it makes sense to also return error (404,408,502,503 and 504) in the same format.
Any `kubernetes` service with the label `content=json` will return a json response in case of an error.

```
docker run \
  --name kube-haproxy \
  --rm \
  -p 80:80 \
  -p 443:443 \
  -p 1936:1936 \
  -p 2222:2222 \
  -p 8081:8081 \
  aledbf/kube-haproxy-router:v0.0.1 \
  /kube-haproxy \
  --master http://$(etcdctl get /deis/scheduler/k8s/master):8080 \
  --domain=$(etcdctl get /deis/platform/domain) \
  --nodes=$(fleetctl list-machines -fields=ip -no-legend | xargs | sed -e 's/ /,/g')
```

