package proxy

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func runTest(k8s *k8sServiceProxy, svcWatcher, endpointWatcher watch.Interface, wg *sync.WaitGroup) {
	for {
		if !k8s.runOnce(svcWatcher, endpointWatcher) {
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

func newTestProxy(wg *sync.WaitGroup) (*k8sServiceProxy, *watch.FakeWatcher, *watch.FakeWatcher) {
	k8s := &k8sServiceProxy{
		pathHandlers:   make(map[string][]http.Handler),
		services:       make(map[string]*svcEndpoint),
		endpoints:      make(map[string]*endpointData),
		defaultHandler: http.NotFoundHandler(),
		makeServiceURL: makeTestURL,
	}

	svcWatcher := watch.NewFake()
	endpointWatcher := watch.NewFake()
	wg.Add(1)

	go runTest(k8s, svcWatcher, endpointWatcher, wg)

	return k8s, svcWatcher, endpointWatcher
}

func TestServiceAdd(t *testing.T) {
	var wg sync.WaitGroup
	k8s, watcher, _ := newTestProxy(&wg)

	watcher.Add(
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "foo"},
		})

	watcher.Add(
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
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
	k8s, watcher, _ := newTestProxy(&wg)

	services := []*v1.Service{
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   "default",
				Name:        "foo",
				Annotations: map[string]string{SvcProxyAnnotationPath: "xxx"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   "default",
				Name:        "bar",
				Annotations: map[string]string{SvcProxyAnnotationPath: "xxy"},
			},
			Spec: v1.ServiceSpec{
				Ports: []v1.ServicePort{{Port: 8080}},
			},
		},
	}

	for _, obj := range services {
		watcher.Add(obj)
	}

	watcher.Delete(
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "foo"},
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
	k8s, svcWatcher, endpointWatcher := newTestProxy(&wg)

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "xxx"},
		},
	}

	svcWatcher.Add(svc)
	svcWatcher.Stop()
	wg.Wait()

	svcWatcher = watch.NewFake()
	wg.Add(1)
	go runTest(k8s, svcWatcher, endpointWatcher, &wg)

	svc.ObjectMeta.Annotations[SvcProxyAnnotationPath] = "yyy"
	svcWatcher.Modify(svc)

	svcWatcher.Stop()
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

func TestServicePathSwap(t *testing.T) {
	var wg sync.WaitGroup
	k8s, svcWatcher, endpointWatcher := newTestProxy(&wg)

	svcA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-a",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "prod/foo"},
		},
	}
	svcB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-b",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "staging/foo"},
		},
	}

	svcWatcher.Add(svcA)
	svcWatcher.Add(svcB)
	svcWatcher.Stop()
	wg.Wait()

	svcA.ObjectMeta.Annotations[SvcProxyAnnotationPath], svcB.ObjectMeta.Annotations[SvcProxyAnnotationPath] =
		svcB.ObjectMeta.Annotations[SvcProxyAnnotationPath], svcA.ObjectMeta.Annotations[SvcProxyAnnotationPath]

	svcWatcher = watch.NewFake()
	go runTest(k8s, svcWatcher, endpointWatcher, &wg)

	wg.Add(1)
	svcWatcher.Modify(svcA)
	svcWatcher.Modify(svcB)
	svcWatcher.Stop()
	wg.Wait()

	var paths []string
	for path, hlist := range k8s.pathHandlers {
		paths = append(paths, path)
		if len(hlist) != 1 {
			t.Error(path, len(hlist))
		}
	}
	sort.Strings(paths)

	expected := []string{
		"prod/foo",
		"staging/foo",
	}
	if !reflect.DeepEqual(paths, expected) {
		t.Error(paths)
	}
}

func TestServicePathChangeDelete(t *testing.T) {
	var wg sync.WaitGroup
	k8s, svcWatcher, endpointWatcher := newTestProxy(&wg)

	svcA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-a",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "prod/foo"},
		},
	}
	svcB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-b",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "staging/foo"},
		},
	}

	svcWatcher.Add(svcA)
	svcWatcher.Add(svcB)
	svcWatcher.Stop()
	wg.Wait()

	svcB.ObjectMeta.Annotations[SvcProxyAnnotationPath] = svcA.ObjectMeta.Annotations[SvcProxyAnnotationPath]

	svcWatcher = watch.NewFake()
	go runTest(k8s, svcWatcher, endpointWatcher, &wg)

	wg.Add(1)
	svcWatcher.Modify(svcB)
	svcWatcher.Delete(svcA)
	svcWatcher.Stop()
	wg.Wait()

	var paths []string
	for path := range k8s.pathHandlers {
		paths = append(paths, path)
	}

	expected := []string{
		"prod/foo",
	}
	if !reflect.DeepEqual(paths, expected) {
		t.Error(paths)
	}
}

func TestServiceAddPathConflict(t *testing.T) {
	var wg sync.WaitGroup
	k8s, svcWatcher, endpointWatcher := newTestProxy(&wg)

	svcA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-a",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "prod/foo"},
		},
	}
	svcB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-b",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "prod/foo"},
		},
	}

	svcWatcher.Add(svcA)
	svcWatcher.Add(svcB)
	svcWatcher.Stop()
	wg.Wait()

	svcA.ObjectMeta.Annotations[SvcProxyAnnotationPath] = "staging/foo"
	svcWatcher = watch.NewFake()
	go runTest(k8s, svcWatcher, endpointWatcher, &wg)

	wg.Add(1)
	svcWatcher.Modify(svcA)

	svcWatcher.Stop()
	wg.Wait()

	var paths []string
	for path := range k8s.pathHandlers {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	expected := []string{
		"prod/foo",
		"staging/foo",
	}
	if !reflect.DeepEqual(paths, expected) {
		t.Error(paths)
	}
}

func TestServiceAddPathConflict2(t *testing.T) {
	var wg sync.WaitGroup
	k8s, svcWatcher, endpointWatcher := newTestProxy(&wg)

	svcA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-a",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "prod/foo"},
		},
	}
	svcB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-b",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationPath: "prod/foo"},
		},
	}

	svcWatcher.Add(svcA)
	svcWatcher.Add(svcB)
	svcWatcher.Stop()
	wg.Wait()

	svcB.ObjectMeta.Annotations[SvcProxyAnnotationPath] = "staging/foo"
	svcWatcher = watch.NewFake()
	go runTest(k8s, svcWatcher, endpointWatcher, &wg)

	wg.Add(1)
	svcWatcher.Modify(svcB)

	svcWatcher.Stop()
	wg.Wait()

	var paths []string
	for path := range k8s.pathHandlers {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	expected := []string{
		"prod/foo",
		"staging/foo",
	}
	if !reflect.DeepEqual(paths, expected) {
		t.Error(paths)
	}
}

func TestMapProxy(t *testing.T) {
	var pathlist []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathlist = append(pathlist, r.URL.Path)
	}))
	defer server.Close()

	backendAddr := server.Listener.Addr()
	backendAddrPieces := strings.Split(backendAddr.String(), ":")

	var wg sync.WaitGroup
	k8s, watcher, _ := newTestProxy(&wg)
	watcher.Add(
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
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
	wg.Wait()

	expected := []string{"/bar/", "/bar/x"}

	requestPaths := []string{
		"http://example.com/foo",
		"http://example.com/foo/",
		"http://example.com/foo/x",
	}
	for _, p := range requestPaths {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", p, nil)
		k8s.ServeHTTP(w, req)
		log.Printf("%s: %d", p, w.Code)
	}

	if !reflect.DeepEqual(expected, pathlist) {
		t.Error(pathlist)
	}
}

func TestEndpointAddDelete(t *testing.T) {
	var wg sync.WaitGroup
	k8s, svcWatch, endpointWatch := newTestProxy(&wg)

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationEndpoint: "8080"},
		},
	}

	endpoints := &v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "foo",
		},
		Subsets: []v1.EndpointSubset{
			{
				Addresses: []v1.EndpointAddress{
					{
						IP: "127.0.0.1",
						TargetRef: &v1.ObjectReference{
							Kind: "Pod",
							Name: "foo-xyz",
						},
					},
				},
			},
			{
				NotReadyAddresses: []v1.EndpointAddress{
					{
						IP: "8.8.8.8",
						TargetRef: &v1.ObjectReference{
							Kind: "Pod",
							Name: "foo-aaa",
						},
					},
				},
			},
		},
	}

	svcWatch.Add(svc)
	endpointWatch.Add(endpoints)
	svcWatch.Stop()
	endpointWatch.Stop()
	wg.Wait()

	podEndpoints, exists := k8s.endpoints["default/foo"]
	if !exists {
		t.Fatal("No endpoints present")
	}

	var actual []string
	for _, ep := range podEndpoints.endpoints {
		actual = append(actual, ep.PodName)
	}

	expected := []string{"foo-aaa", "foo-xyz"}
	if !reflect.DeepEqual(actual, expected) {
		t.Error(actual)
	}

	svcWatch = watch.NewFake()
	endpointWatch = watch.NewFake()
	wg.Add(1)
	go runTest(k8s, svcWatch, endpointWatch, &wg)

	endpointWatch.Delete(endpoints)
	endpointWatch.Stop()
	wg.Wait()

	if len(podEndpoints.endpoints) != 0 {
		t.Error(podEndpoints.endpoints)
	}
}

func TestEndpointProxy(t *testing.T) {
	var pathlist []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathlist = append(pathlist, r.URL.Path)
	}))
	defer server.Close()

	backendAddr := server.Listener.Addr()
	backendAddrPieces := strings.Split(backendAddr.String(), ":")
	log.Print("backend ", backendAddr.String())

	var wg sync.WaitGroup
	k8s, svcWatch, endpointWatch := newTestProxy(&wg)

	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "foo",
			Annotations: map[string]string{SvcProxyAnnotationEndpoint: backendAddrPieces[1]},
		},
	}

	endpoints := &v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "foo",
		},
		Subsets: []v1.EndpointSubset{
			{
				Addresses: []v1.EndpointAddress{
					{
						IP: "127.0.0.1",
						TargetRef: &v1.ObjectReference{
							Kind: "Pod",
							Name: "foo-xyz",
						},
					},
				},
			},
		},
	}

	svcWatch.Add(svc)
	endpointWatch.Add(endpoints)

	svcWatch.Stop()
	wg.Wait()

	if len(k8s.endpoints) != 1 {
		t.Fatal(len(k8s.endpoints))
	}

	expected := []string{"/debug/varz", "/"}

	requestPaths := []string{
		"http://localhost/endpoint/default/foo/0/debug/varz",
		"http://localhost/endpoint/default/foo/0/",
	}
	for _, p := range requestPaths {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", p, nil)
		k8s.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Error(w.Body.String())
		}
	}

	if !reflect.DeepEqual(expected, pathlist) {
		t.Error(pathlist)
	}

	badRequests := []string{
		"http://localhost/endpoint/default/bar/0/debug/varz",
		"http://localhost/endpoint/default/foo/1",
		"http://localhost/endpoint/default/foo",
	}
	for _, p := range badRequests {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", p, nil)
		k8s.ServeHTTP(w, req)
		log.Printf("%s %d", p, w.Code)
		if w.Code == http.StatusOK {
			t.Error(w.Code)
		}
	}

}

func mapRedirectTest(t *testing.T, srcPath, dstPath string) {
	server := httptest.NewServer(http.RedirectHandler("index.html", http.StatusSeeOther))
	defer server.Close()

	backendAddr := server.Listener.Addr()
	backendAddrPieces := strings.Split(backendAddr.String(), ":")

	var wg sync.WaitGroup
	k8s, watcher, _ := newTestProxy(&wg)
	watcher.Add(
		&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "foo",
				Annotations: map[string]string{
					SvcProxyAnnotationPath: srcPath,
					SvcProxyAnnotationPort: backendAddrPieces[1],
					SvcProxyAnnotationMap:  dstPath,
				},
			},
		})

	watcher.Stop()
	wg.Wait()

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", srcPath, nil)
	k8s.ServeHTTP(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Error(resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	expect := path.Join(srcPath, "index.html")
	if location != expect {
		t.Errorf("Expected %s, got %s", expect, location)
	}
}

func TestMappedRedirect(t *testing.T) {
	testCases := []struct {
		srcPath string
		dstPath string
	}{
		{"/foo/", "/bar/"},
		{"/foo/bar/", "/"},
		{"/foo", "/bar/"},
	}
	for _, test := range testCases {
		name := fmt.Sprintf("%s => %s", test.srcPath, test.dstPath)
		t.Run(name, func(t *testing.T) {
			mapRedirectTest(t, test.srcPath, test.dstPath)
		})
	}
}
