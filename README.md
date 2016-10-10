# k8s-service-proxy

[![Build Status](https://travis-ci.org/pedro-r-marques/k8s-service-proxy.svg?branch=master)](https://travis-ci.org/pedro-r-marques/k8s-service-proxy)

HTTP Proxy for kubernetes services

This process implements a simple HTTP proxy based on the golang [httputil] [ReverseProxy]
class. It auto-discovers the HTTP backends based on Annotations of the Kubernetes Services objects.
It can be used in conjunction with an oauth2 proxy (e.g. [oauth2]) to provide acccess controlled
access to services.

For instance, it is common to run internal/debug HTTP ports that need to be access controlled; in
in order to provide access to such information, it is useful to be able to expose a single external
service that performs authentication and demuxes requests to the backends.

The service proxy expects services to define annotations such as:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: example
  annotations:
    k8s-svc-proxy.local/path: /example-path/
```

This would case the proxy to direct all traffic for `"/example-path/"` to the "example" service. The
receiving pods are expected to process requests that include `"/example-path/"`

For services that expose a single port, the proxy will automatically use the port number defined in
the service configuration. Services that expose multiple ports are expected to use the
annotation `k8s-svc-proxy.local/port` to specify the port number for the redirected traffic.

URLs can be remapped by specifying the annotation `k8s-svc-proxy.local/map`. This causes the `path` prefix
of a request to be replaced with the string specified by `map`. Note that the HTTP response is not
processed in anyway. Any absolute `href` URLs will be incorrect.

For diagnostic purposes, the proxy serves a status page. The annotation `k8s-svc-proxy.local/description`
can be used to add human readable content to this page.

## Endpoints

Services are often implemented by multiple Pods. These pods often have http listeners that provide information specific
to the Pod (e.g. /debug). The annotation `"k8s-svc-proxy.local/endpoint-port"` automatically exposes the specified
port in all the endpoints of the service as `"/endpoint/<namespace>/<svc-name>/<id>"` where id is an index automatically assigned
by the alphabetic order of pod names.

## Example configuration

- k8s deployment:

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: oauth2-proxy
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: oauth2-proxy
    spec:
      containers:
        - name: oauth2-proxy
          image: oauth2_proxy
          env:
            - name: OAUTH2_PROXY_CLIENT_ID
              valueFrom:
                secretKeyRef:
                  name: oauth2-proxy
                  key: client-id
            - name: OAUTH2_PROXY_CLIENT_SECRET
              valueFrom:
                secretKeyRef:
                  name: oauth2-proxy
                  key: client-secret
          ports:
            - containerPort: 4180
              name: oauth2-proxy
        - name: k8s-svc-proxy
          image: k8s-svc-proxy
          ports:
            - containerPort: 8080

```

- etc/oauth2_proxy.cfg

```text
http_address = "0.0.0.0:4180"

email_domains = [
    "example.com"
]

upstreams = [
    "http://localhost:8080/",
]
```

[oauth2]: https://github.com/bitly/oauth2_proxy
[httputil]: https://golang.org/pkg/net/http/httputil/
[ReverseProxy]: https://golang.org/pkg/net/http/httputil/#ReverseProxy
