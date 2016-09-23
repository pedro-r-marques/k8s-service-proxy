package proxy

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/1.4/pkg/api/v1"
	"k8s.io/client-go/1.4/pkg/watch"
)

func runTest(k8s *k8sServiceProxy, watcher watch.Interface, wg *sync.WaitGroup) {
	for {
		if !k8s.runOnce(watcher) {
			break
		}
	}
	wg.Done()
}

func makeTestURL(svc *v1.Service, endpoint *svcEndpoint) *url.URL {
	schemeHost := fmt.Sprintf("http://localhost")
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

func newTestProxy(wg *sync.WaitGroup) (*k8sServiceProxy, *watch.FakeWatcher) {
	k8s := &k8sServiceProxy{
		pathHandlers:   make(map[string]http.Handler),
		services:       make(map[string]*svcEndpoint),
		defaultHandler: http.NotFoundHandler(),
		makeServiceURL: makeTestURL,
	}

	watcher := watch.NewFake()
	wg.Add(1)

	go runTest(k8s, watcher, wg)

	return k8s, watcher
}

func TestServiceAdd(t *testing.T) {
	var wg sync.WaitGroup
	k8s, watcher := newTestProxy(&wg)

	watcher.Add(
		&v1.Service{
			ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "foo"},
		})

	watcher.Add(
		&v1.Service{
			ObjectMeta: v1.ObjectMeta{
				Namespace:   "default",
				Name:        "bar",
				Annotations: map[string]string{SvcProxyAnnotationPath: "xxx"},
			},
		})

	watcher.Stop()
	wg.Wait()

	if len(k8s.services) != 1 {
		t.Errorf("Expected 1 service, got %d", len(k8s.services))
	}
}

func TestServiceDelete(t *testing.T) {
	var wg sync.WaitGroup
	k8s, watcher := newTestProxy(&wg)

	services := []*v1.Service{
		&v1.Service{
			ObjectMeta: v1.ObjectMeta{
				Namespace:   "default",
				Name:        "foo",
				Annotations: map[string]string{SvcProxyAnnotationPath: "xxx"},
			},
		},
		&v1.Service{
			ObjectMeta: v1.ObjectMeta{
				Namespace:   "default",
				Name:        "bar",
				Annotations: map[string]string{SvcProxyAnnotationPath: "xxy"},
			},
			Spec: v1.ServiceSpec{
				Ports: []v1.ServicePort{v1.ServicePort{Port: 8080}},
			},
		},
	}

	for _, obj := range services {
		watcher.Add(obj)
	}

	watcher.Delete(
		&v1.Service{
			ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "foo"},
		})

	watcher.Stop()
	wg.Wait()

	if len(k8s.services) != 1 {
		t.Errorf("Expected 1 service, got %d", len(k8s.services))
	}

	if endpoint, exists := k8s.services["default/bar"]; exists {
		if endpoint.Port != 8080 {
			t.Errorf("Expected port 8080, got %d", endpoint.Port)
		}
	} else {
		t.Error("Service not found")
	}
}

func TestServiceChange(t *testing.T) {
	var wg sync.WaitGroup
	k8s, watcher := newTestProxy(&wg)

	svc := &v1.Service{
		ObjectMeta: v1.ObjectMeta{
			Namespace:   "default",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "xxx"},
		},
	}

	watcher.Add(svc)
	watcher.Stop()
	wg.Wait()

	watcher = watch.NewFake()
	wg.Add(1)
	go runTest(k8s, watcher, &wg)

	svc.ObjectMeta.Annotations[SvcProxyAnnotationPath] = "yyy"
	watcher.Modify(svc)

	watcher.Stop()
	wg.Wait()

	if len(k8s.services) != 1 {
		t.Errorf("Expected 1 service, got %d", len(k8s.services))
	}
	if len(k8s.pathHandlers) != 1 {
		t.Errorf("Expected 1 paths, got %d", len(k8s.pathHandlers))
	}
	path := svc.ObjectMeta.Annotations[SvcProxyAnnotationPath]
	if _, exists := k8s.pathHandlers[path]; !exists {
		t.Error(path)
	}
}

func TestMapProxy(t *testing.T) {
	var pathlist []string
	var rqWait sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathlist = append(pathlist, r.URL.Path)
		rqWait.Done()
	}))
	defer server.Close()

	backendAddr := server.Listener.Addr()
	backendAddrPieces := strings.Split(backendAddr.String(), ":")

	var wg sync.WaitGroup
	k8s, watcher := newTestProxy(&wg)
	wg.Add(1)
	watcher.Add(
		&v1.Service{
			ObjectMeta: v1.ObjectMeta{
				Namespace: "default",
				Name:      "foo",
				Annotations: map[string]string{
					SvcProxyAnnotationPath: "/foo/",
					SvcProxyAnnotationPort: backendAddrPieces[1],
					SvcProxyAnnotationMap:  "/bar/",
				},
			},
		})

	watcher.Stop()
	wg.Done()

	expected := []string{"/bar/", "/bar/x"}
	rqWait.Add(len(expected))

	w := httptest.NewRecorder()
	requestPaths := []string{
		"http://example.com/foo",
		"http://example.com/foo/",
		"http://example.com/foo/x",
	}
	for _, p := range requestPaths {
		req, _ := http.NewRequest("GET", p, nil)
		k8s.ServeHTTP(w, req)
	}

	rqWait.Wait()

	if !reflect.DeepEqual(expected, pathlist) {
		t.Error(pathlist)
	}
}
