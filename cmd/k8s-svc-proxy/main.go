package main

import (
	_ "expvar"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"path/filepath"

	"github.com/pedro-r-marques/k8s-service-proxy/pkg/proxy"
)

type options struct {
	Port          int
	HTTPStaticDir string
}

func defineFlags(opt *options) {
	flag.IntVar(&opt.Port, "port", 8080, "Listening port")
	flag.StringVar(&opt.HTTPStaticDir, "http-static-dir", "/var/www", "Directory for static http content")
}

func defaultMuxServeHTTP(w http.ResponseWriter, r *http.Request) {
	req := r.Clone(r.Context())
	req.URL.Path = req.URL.Path[len(proxy.SvcProxyHTTPPath)-1:]
	http.DefaultServeMux.ServeHTTP(w, req)
}

func main() {
	var opt options
	defineFlags(&opt)
	flag.Parse()

	log.Print("Listening on port ", opt.Port)

	mux := http.NewServeMux()
	mux.Handle(proxy.SvcProxyHTTPPath, http.FileServer(http.Dir(opt.HTTPStaticDir)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, proxy.SvcProxyHTTPPath+"status.html", http.StatusSeeOther)
		} else {
			w.WriteHeader(http.StatusNotFound)
			if msg, err := ioutil.ReadFile(filepath.Join(opt.HTTPStaticDir, proxy.SvcProxyHTTPPath+"error_404.html")); err == nil {
				w.Write(msg)
			} else {
				log.Println(err)
			}
		}
	})

	mux.HandleFunc(proxy.SvcProxyHTTPPath+"debug/", defaultMuxServeHTTP)

	svcProxy := proxy.NewKubernetesServiceProxy(mux)
	http.ListenAndServe(fmt.Sprintf(":%d", opt.Port), svcProxy)
}
