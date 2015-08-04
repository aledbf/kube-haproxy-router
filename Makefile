
REGISTRY = $(shell if [ "$$DEV_REGISTRY" == "registry.hub.docker.com" ]; then echo; else echo $$DEV_REGISTRY/; fi)
GIT_SHA = $(shell git rev-parse --short HEAD)
IMAGE = kube-haproxy-router:$(BUILD_TAG)
DEV_IMAGE = $(REGISTRY)$(IMAGE)

ifndef BUILD_TAG
  BUILD_TAG = git-$(GIT_SHA)
endif

all: build

build: kube-haproxy.go
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 godep go build -a -installsuffix cgo -ldflags '-w' ./kube-haproxy.go

image: build
	docker build -t $(IMAGE) .

push: image
	docker push $(IMAGE)

clean:
	rm -f kube-haproxy
