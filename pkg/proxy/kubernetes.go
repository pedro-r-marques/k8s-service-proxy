package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/1.4/kubernetes"
	"k8s.io/client-go/1.4/pkg/api"
	"k8s.io/client-go/1.4/pkg/api/v1"
	"k8s.io/client-go/1.4/pkg/watch"
	"k8s.io/client-go/1.4/rest"
)

// SvcProxyHTTPPath is the http request path served by the proxy itself.
const SvcProxyHTTPPath = "/k8s-svc-proxy/"

type svcEndpoint struct {
	Path        string
	Port        int32
	Map         string
	Description string
}

type k8sServiceProxy struct {
	sync.Mutex
	pathHandlers   map[string]http.Handler
	services       map[string]*svcEndpoint
	defaultHandler http.Handler
	makeServiceURL func(*v1.Service, *svcEndpoint) *url.URL
}

const (
	// SvcProxyAnnotationPrefix defines the annotation prefix used by the proxy.
	SvcProxyAnnotationPrefix = "k8s-svc-proxy.local/"

	// SvcProxyAnnotationPath is the annotation used by a kubernetes service
	// to add a specific string to the list of paths handled by this proxy.
	SvcProxyAnnotationPath = SvcProxyAnnotationPrefix + "path"

	// SvcProxyAnnotationPort (optional) specifies the HTTP port to forward traffic to.
	SvcProxyAnnotationPort = SvcProxyAnnotationPrefix + "port"

	// SvcProxyAnnotationMap (optional) specifies a URL mapping by prefix.
	SvcProxyAnnotationMap = SvcProxyAnnotationPrefix + "map"

	// SvcProxyAnnotationDescription (optional) defines a human readable description for the service.
	SvcProxyAnnotationDescription = SvcProxyAnnotationPrefix + "description"

	discoveryStatusPage = SvcProxyHTTPPath + "discovery"
)

func requestMapper(endpoint *svcEndpoint, target *url.URL, req *http.Request) {
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = endpoint.Map + req.URL.Path[len(endpoint.Path):]
	// explicitly disable User-Agent so it's not set to default value
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "")
	}
}

func (k *k8sServiceProxy) newProxyHandler(target *url.URL, endpoint *svcEndpoint) http.Handler {
	var proxy http.Handler
	if endpoint.Map != "" {
		director := func(req *http.Request) {
			requestMapper(endpoint, target, req)
		}
		proxy = &httputil.ReverseProxy{Director: director}
	} else {
		proxy = httputil.NewSingleHostReverseProxy(target)
	}
	return proxy
}

// ServeHttp implements the http.Handler interface.
// It is called to demux request paths.
func (k *k8sServiceProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case discoveryStatusPage:
		k.statusPage(rw, req)
		return
	}

	var bestMatch string
	handler := k.defaultHandler
	k.Lock()
	for k, v := range k.pathHandlers {
		if strings.HasPrefix(req.URL.Path, k) && len(k) > len(bestMatch) {
			bestMatch = k
			handler = v
		}
	}
	k.Unlock()

	handler.ServeHTTP(rw, req)
}

func (k *k8sServiceProxy) statusPage(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(k.services)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

func getServicePort(svc *v1.Service) (int32, bool) {
	if port, exists := svc.Annotations[SvcProxyAnnotationPort]; exists {
		p, err := strconv.Atoi(port)
		if err != nil {
			log.Printf("Invalid annotation (%s) for %s/%s", port, svc.Namespace, svc.Name)
			return -1, false
		}
		return int32(p), true
	} else if len(svc.Spec.Ports) == 1 {
		svcPort := svc.Spec.Ports[0]
		if svcPort.Port != 80 {
			return svcPort.Port, true
		}
	}
	return -1, false
}

func makeSvcEndpoint(svc *v1.Service) *svcEndpoint {
	path, exists := svc.Annotations[SvcProxyAnnotationPath]
	if !exists {
		return nil
	}
	if strings.HasPrefix(path, SvcProxyHTTPPath) {
		return nil
	}
	endpoint := &svcEndpoint{
		Path: path,
		Port: -1,
	}
	if port, isSet := getServicePort(svc); isSet {
		endpoint.Port = port
	}
	if mapPrefix, isSet := svc.Annotations[SvcProxyAnnotationMap]; isSet {
		endpoint.Map = mapPrefix
	}
	if desc, isSet := svc.Annotations[SvcProxyAnnotationDescription]; isSet {
		endpoint.Description = desc
	}
	return endpoint
}

func makeServiceURL(svc *v1.Service, endpoint *svcEndpoint) *url.URL {
	schemeHost := fmt.Sprintf("http://%s.%s.svc", svc.Name, svc.Namespace)
	if endpoint.Port >= 0 {
		schemeHost += fmt.Sprintf(":%d", endpoint.Port)
	}

	u, err := url.Parse(schemeHost)
	if err != nil {
		log.Print(err)
		return nil
	}
	return u
}

func (k *k8sServiceProxy) serviceAdd(svc *v1.Service) {
	endpoint := makeSvcEndpoint(svc)
	if endpoint == nil {
		return
	}

	svcID := svc.Namespace + "/" + svc.Name
	log.Print("ADD service ", svcID)

	if prev, dup := k.services[svcID]; dup {
		log.Printf("ADD event for existing service %s", svcID)
		if reflect.DeepEqual(endpoint, prev) {
			return
		}
		delete(k.pathHandlers, endpoint.Path)
	}

	if _, dup := k.pathHandlers[endpoint.Path]; dup {
		log.Printf("Duplicate %s annotation for %s: %s/%s", SvcProxyAnnotationPath, endpoint.Path, svc.Namespace, svc.Name)
		return
	}

	u := k.makeServiceURL(svc, endpoint)
	k.Lock()
	defer k.Unlock()
	k.pathHandlers[endpoint.Path] = k.newProxyHandler(u, endpoint)
	k.services[svcID] = endpoint
}

func (k *k8sServiceProxy) serviceDelete(svc *v1.Service) {
	svcID := svc.Namespace + "/" + svc.Name
	log.Print("DELETE service ", svcID)
	k.Lock()
	defer k.Unlock()
	if endpoint, exists := k.services[svcID]; exists {
		delete(k.services, svcID)
		delete(k.pathHandlers, endpoint.Path)
	}
}

func (k *k8sServiceProxy) serviceChange(svc *v1.Service) {
	svcID := svc.Namespace + "/" + svc.Name
	prev := k.services[svcID]
	endpoint := makeSvcEndpoint(svc)

	if prev != nil && endpoint != nil {
		if reflect.DeepEqual(prev, endpoint) {
			return
		}

		log.Print("CHANGE service ", svcID)
		u := k.makeServiceURL(svc, endpoint)
		k.Lock()
		defer k.Unlock()
		k.pathHandlers[endpoint.Path] = k.newProxyHandler(u, endpoint)
		delete(k.pathHandlers, prev.Path)
		k.services[svcID] = endpoint
	} else if endpoint != nil {
		k.serviceAdd(svc)
	} else if prev != nil {
		k.serviceDelete(svc)
	}
}

func (k *k8sServiceProxy) runOnce(watcher watch.Interface) bool {
	select {
	case ev, ok := <-watcher.ResultChan():
		if !ok {
			return false
		}
		switch ev.Type {
		case watch.Added:
			k.serviceAdd(ev.Object.(*v1.Service))
		case watch.Deleted:
			k.serviceDelete(ev.Object.(*v1.Service))
		case watch.Modified:
			k.serviceChange(ev.Object.(*v1.Service))
		}
	}
	return true
}

const maxWatcherFailures = 3

func (k *k8sServiceProxy) run(clientset *kubernetes.Clientset) {
	watcher, err := clientset.Core().Services("").Watch(api.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	for {
	LOOP:
		ok := k.runOnce(watcher)
		if !ok {
			log.Print("k8s watcher channel closed")
			var err error
			for failures := 0; failures < maxWatcherFailures; failures++ {
				watcher, err = clientset.Core().Services("").Watch(api.ListOptions{})
				if err == nil {
					goto LOOP
				}
				time.Sleep(5 * time.Second)
			}
			log.Fatal(err)
		}
	}
}

// NewKubernetesServiceProxy allocates an http proxy that demuxes URLs
// based on the paths learnt from k8s service annotations.
func NewKubernetesServiceProxy(mux http.Handler) http.Handler {

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	k8s := &k8sServiceProxy{
		pathHandlers:   make(map[string]http.Handler),
		services:       make(map[string]*svcEndpoint),
		defaultHandler: mux,
		makeServiceURL: makeServiceURL,
	}

	go k8s.run(clientset)

	return k8s
}
