package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// SvcProxyHTTPPath is the http request path served by the proxy itself.
const SvcProxyHTTPPath = "/k8s-svc-proxy/"

type svcEndpoint struct {
	Path        string
	Port        int32
	Map         string
	Description string
	handler     http.Handler
}

type podEndpoint struct {
	PodName string
	IP      string
	handler http.Handler
}

type podEndpointSorter []*podEndpoint

func (a podEndpointSorter) Len() int           { return len(a) }
func (a podEndpointSorter) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a podEndpointSorter) Less(i, j int) bool { return a[i].PodName < a[j].PodName }

type endpointData struct {
	Port      int
	endpoints []*podEndpoint
}

type k8sServiceProxy struct {
	sync.Mutex
	pathHandlers   map[string][]http.Handler
	services       map[string]*svcEndpoint
	endpoints      map[string]*endpointData
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

	// SvcProxyAnnotationEndpoint (optional) specifies that the endpoints of the service should be
	// exposed under the /endpoint path.
	SvcProxyAnnotationEndpoint = SvcProxyAnnotationPrefix + "endpoint-port"

	serviceDiscoveryPage  = SvcProxyHTTPPath + "services"
	endpointDiscoveryPage = SvcProxyHTTPPath + "endpoints"

	endpointPath = "/endpoint/"
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

func makeEndpointProxy(scheme, host string) http.Handler {
	director := func(req *http.Request) {
		if scheme == "" {
			scheme = "http"
		}
		req.URL.Scheme = scheme
		req.URL.Host = host
		path := strings.SplitN(req.URL.Path[1:], "/", 5)
		req.URL.Path = "/" + path[4]

		if _, ok := req.Header["User-Agent"]; !ok {
			req.Header.Set("User-Agent", "")
		}
	}
	return &httputil.ReverseProxy{Director: director}
}

// getEndpointWithHandler encloses the portion of serveEndpoint that runs under the lock
// since it access shared datastructures.
func (k *k8sServiceProxy) getEndpointWithHandler(key string, id int, scheme string) *podEndpoint {
	k.Lock()
	defer k.Unlock()

	data, exists := k.endpoints[key]
	if !exists || data.Port <= 0 {
		return nil
	}
	list := data.endpoints
	if id >= len(list) {
		return nil
	}

	endpoint := list[id]
	if endpoint.handler == nil {
		host := fmt.Sprintf("%s:%d", endpoint.IP, data.Port)
		endpoint.handler = makeEndpointProxy(scheme, host)
	}
	return endpoint
}

func (k *k8sServiceProxy) serveEndpoint(w http.ResponseWriter, r *http.Request) {
	// /endpoint/<namespace>/service/id/request-path
	parts := strings.SplitN(r.URL.Path[1:], "/", 5)
	if len(parts) < 5 {
		http.Error(w, r.URL.Path, http.StatusNotFound)
		return
	}
	key := strings.Join(parts[1:3], "/")
	id, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		http.Error(w, parts[3], http.StatusNotFound)
		return
	}

	endpoint := k.getEndpointWithHandler(key, int(id), r.URL.Scheme)
	if endpoint == nil {
		http.Error(w, key, http.StatusNotFound)
		return
	}
	endpoint.handler.ServeHTTP(w, r)
}

// ServeHttp implements the http.Handler interface.
// It is called to demux request paths.
func (k *k8sServiceProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case serviceDiscoveryPage:
		k.serviceStatus(rw, req)
		return
	case endpointDiscoveryPage:
		k.endpointStatus(rw, req)
		return
	}

	if strings.HasPrefix(req.URL.Path, endpointPath) {
		k.serveEndpoint(rw, req)
		return
	}

	var bestMatch string
	handler := k.defaultHandler
	k.Lock()
	for k, v := range k.pathHandlers {
		if strings.HasPrefix(req.URL.Path, k) && len(k) > len(bestMatch) {
			bestMatch = k
			handler = v[0]
		}
	}
	k.Unlock()

	handler.ServeHTTP(rw, req)
}

func (k *k8sServiceProxy) serviceStatus(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(k.services)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

// EndpointStatus is the status information corresponding to service backends.
type EndpointStatus struct {
	Name     string
	Port     int
	Backends []*podEndpoint
}

func (k *k8sServiceProxy) endpointStatus(w http.ResponseWriter, r *http.Request) {
	var endpointStatus []*EndpointStatus
	for k, v := range k.endpoints {
		if v.Port == 0 {
			continue
		}
		endpointStatus = append(endpointStatus, &EndpointStatus{
			Name:     k,
			Port:     v.Port,
			Backends: v.endpoints,
		})
	}

	js, err := json.Marshal(endpointStatus)
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

func handlerListRemove(list []http.Handler, element http.Handler) []http.Handler {
	for i, h := range list {
		if h == element {
			list[i] = list[len(list)-1]
			list = list[:len(list)-1]
			break
		}
	}
	return list
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
	}

	u := k.makeServiceURL(svc, endpoint)
	endpoint.handler = k.newProxyHandler(u, endpoint)

	k.Lock()
	defer k.Unlock()
	k.pathHandlers[endpoint.Path] = append(k.pathHandlers[endpoint.Path], endpoint.handler)
	k.services[svcID] = endpoint
}

func (k *k8sServiceProxy) serviceDelete(svc *v1.Service) {
	svcID := svc.Namespace + "/" + svc.Name
	log.Print("DELETE service ", svcID)
	k.Lock()
	defer k.Unlock()
	if endpoint, exists := k.services[svcID]; exists {
		delete(k.services, svcID)
		k.pathHandlers[endpoint.Path] = handlerListRemove(k.pathHandlers[endpoint.Path], endpoint.handler)
		if len(k.pathHandlers[endpoint.Path]) == 0 {
			delete(k.pathHandlers, endpoint.Path)
		}
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
		endpoint.handler = k.newProxyHandler(u, endpoint)
		k.Lock()
		defer k.Unlock()
		k.pathHandlers[prev.Path] = handlerListRemove(k.pathHandlers[prev.Path], prev.handler)
		k.pathHandlers[endpoint.Path] = append(k.pathHandlers[endpoint.Path], endpoint.handler)
		if len(k.pathHandlers[prev.Path]) == 0 {
			delete(k.pathHandlers, prev.Path)
		}
		k.services[svcID] = endpoint
	} else if endpoint != nil {
		k.serviceAdd(svc)
	} else if prev != nil {
		k.serviceDelete(svc)
	}
}

func getEndpointPort(svc *v1.Service) int {
	endpointPort, exists := svc.Annotations[SvcProxyAnnotationEndpoint]
	if !exists {
		return -1
	}
	v, err := strconv.ParseUint(endpointPort, 10, 32)
	if err != nil || v > uint64(^uint16(0)) {
		return -1
	}
	return int(v)
}

func (k *k8sServiceProxy) setEndpointPort(svc *v1.Service, port int) {
	svcID := svc.Namespace + "/" + svc.Name

	k.Lock()
	defer k.Unlock()
	data, exists := k.endpoints[svcID]
	if !exists {
		data = &endpointData{}
		k.endpoints[svcID] = data
	}
	data.Port = port
}

func (k *k8sServiceProxy) addEndpointPort(svc *v1.Service) {
	port := getEndpointPort(svc)
	if port <= 0 {
		return
	}
	k.setEndpointPort(svc, port)
}

func (k *k8sServiceProxy) updateEndpointPort(svc *v1.Service) {
	port := getEndpointPort(svc)
	if port > 0 {
		k.setEndpointPort(svc, port)
	} else {
		k.deleteEndpointPort(svc)
	}
}

func (k *k8sServiceProxy) deleteEndpointPort(svc *v1.Service) {
	svcID := svc.Namespace + "/" + svc.Name

	k.Lock()
	defer k.Unlock()
	if data, exists := k.endpoints[svcID]; exists {
		data.Port = 0
	}
}

func makeEndpointSubList(addresses []v1.EndpointAddress) []*podEndpoint {
	var endpoints []*podEndpoint
	for _, e := range addresses {
		var podName string
		if e.TargetRef != nil && e.TargetRef.Kind == "Pod" {
			podName = e.TargetRef.Name
		}
		endpoints = append(endpoints, &podEndpoint{
			IP:      e.IP,
			PodName: podName,
		})
	}
	return endpoints
}

func makeEndpointList(endpoint *v1.Endpoints) []*podEndpoint {
	var endpoints []*podEndpoint
	for _, subset := range endpoint.Subsets {
		endpoints = append(endpoints, makeEndpointSubList(subset.Addresses)...)
		endpoints = append(endpoints, makeEndpointSubList(subset.NotReadyAddresses)...)
	}
	sort.Sort(podEndpointSorter(endpoints))
	return endpoints
}

func (k *k8sServiceProxy) setEndpointList(endpointObj *v1.Endpoints, endpointList []*podEndpoint) {
	svcID := endpointObj.Namespace + "/" + endpointObj.Name

	k.Lock()
	defer k.Unlock()
	data, exists := k.endpoints[svcID]
	if !exists {
		data = &endpointData{}
		k.endpoints[svcID] = data
	}
	data.endpoints = endpointList
}

func (k *k8sServiceProxy) endpointUpdate(endpoint *v1.Endpoints) {
	endpointList := makeEndpointList(endpoint)
	k.setEndpointList(endpoint, endpointList)
}

func (k *k8sServiceProxy) endpointDelete(endpoint *v1.Endpoints) {
	k.setEndpointList(endpoint, nil)
}

func (k *k8sServiceProxy) runOnce(svcWatcher, endpointWatcher watch.Interface) bool {
	select {
	case ev, ok := <-svcWatcher.ResultChan():
		if !ok {
			return false
		}
		switch ev.Type {
		case watch.Added:
			k.serviceAdd(ev.Object.(*v1.Service))
			k.addEndpointPort(ev.Object.(*v1.Service))
		case watch.Deleted:
			k.serviceDelete(ev.Object.(*v1.Service))
			k.deleteEndpointPort(ev.Object.(*v1.Service))
		case watch.Modified:
			k.serviceChange(ev.Object.(*v1.Service))
			k.updateEndpointPort(ev.Object.(*v1.Service))
		}
	case ev, ok := <-endpointWatcher.ResultChan():
		if !ok {
			return false
		}
		switch ev.Type {
		case watch.Added:
			k.endpointUpdate(ev.Object.(*v1.Endpoints))
		case watch.Deleted:
			k.endpointDelete(ev.Object.(*v1.Endpoints))
		case watch.Modified:
			k.endpointUpdate(ev.Object.(*v1.Endpoints))
		}
	}
	return true
}

const maxWatcherFailures = 3

func createWatchers(clientset *kubernetes.Clientset) (watch.Interface, watch.Interface, error) {
	svcWatcher, err := clientset.CoreV1().Services("").Watch(metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	endpointWatcher, err := clientset.CoreV1().Endpoints("").Watch(metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	return svcWatcher, endpointWatcher, nil
}

func (k *k8sServiceProxy) run(clientset *kubernetes.Clientset) {
	svcWatcher, endpointWatcher, err := createWatchers(clientset)
	if err != nil {
		log.Fatal(err)
	}

	for {
	LOOP:
		ok := k.runOnce(svcWatcher, endpointWatcher)
		if !ok {
			log.Print("k8s watcher channel closed")
			var err error
			for failures := 0; failures < maxWatcherFailures; failures++ {
				svcWatcher, endpointWatcher, err = createWatchers(clientset)
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
		pathHandlers:   make(map[string][]http.Handler),
		services:       make(map[string]*svcEndpoint),
		endpoints:      make(map[string]*endpointData),
		defaultHandler: mux,
		makeServiceURL: makeServiceURL,
	}

	go k8s.run(clientset)

	return k8s
}
