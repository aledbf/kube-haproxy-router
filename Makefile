all: push

# 0.0 shouldn't clobber any released builds
TAG = 0.0
BUILD_IMAGE = build-haproxy
HAPROXY_IMAGE = aledbf/kube-haproxy

server:
	CGO_ENABLED=0 GOOS=linux godep go build -a -installsuffix cgo -ldflags '-w' -o service-loadbalancer ./service_loadbalancer.go ./controller.go ./ingress_controller.go ./service.go

container: server haproxy
	docker build -t $(HAPROXY_IMAGE):$(TAG) .

push: container
	echo "docker push $(HAPROXY_IMAGE):$(TAG)""

haproxy:
	docker build -t $(BUILD_IMAGE):$(TAG) build
	docker create --name $(BUILD_IMAGE) $(BUILD_IMAGE):$(TAG) true
	# docker cp semantics changed between 1.7 and 1.8, so we cp the file to cwd and rename it.
	docker cp $(BUILD_IMAGE):/work/x86_64/haproxy-1.6-r0.apk .
	docker rm -f $(BUILD_IMAGE)
	mv haproxy-1.6-r0.apk haproxy.apk

clean:
	rm -f service_loadbalancer haproxy.apk
	# remove servicelb and contrib-haproxy images
	docker rmi -f $(BUILD_IMAGE):$(TAG) || true
	docker rmi -f $(HAPROXY_IMAGE):$(TAG) || true
