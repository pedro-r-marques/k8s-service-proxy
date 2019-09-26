FROM golang:1.13 as build
ADD . /build/k8s-service-proxy
WORKDIR /build/k8s-service-proxy
RUN go install -ldflags "-linkmode external -extldflags -static" ./cmd/...

FROM alpine:3
ADD cmd/k8s-svc-proxy/static /var/www/k8s-svc-proxy
RUN apk add ca-certificates
COPY --from=build /go/bin/k8s-svc-proxy /usr/local/bin
ENTRYPOINT ["/usr/local/bin/k8s-svc-proxy"]
EXPOSE 8080