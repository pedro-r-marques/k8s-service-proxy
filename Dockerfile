FROM golang:1.6
ADD . /go/src/github.com/pedro-r-marques/k8s-service-proxy
RUN go install github.com/pedro-r-marques/k8s-service-proxy/cmd/...
RUN rm -rf /go/src
ADD cmd/k8s-svc-proxy/static /var/www/k8s-svc-proxy
ENTRYPOINT /go/bin/k8s-svc-proxy
EXPOSE 8080