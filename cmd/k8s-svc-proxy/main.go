package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

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
		http.Redirect(w, r, proxy.SvcProxyHTTPPath+"status.html", http.StatusSeeOther)
	})
	svcProxy := proxy.NewKubernetesServiceProxy(mux)
	http.ListenAndServe(fmt.Sprintf(":%d", opt.Port), svcProxy)
}
