package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
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
	svcProxy := proxy.NewKubernetesServiceProxy(mux)
	http.ListenAndServe(fmt.Sprintf(":%d", opt.Port), svcProxy)
}
