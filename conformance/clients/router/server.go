// Isolated fixture: the actual framed Go service reads fake local model hosts.
package main

import (
	"context"
	"flag"
	"fmt"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/service"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
)

func main() {
	endpoint := flag.String("endpoint", "", "isolated framed endpoint")
	trace := flag.String("trace", "", "HTTP method trace")
	empty := flag.Bool("empty", false, "survey no hosts")
	flag.Parse()
	out, err := os.Create(*trace)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	var mu sync.Mutex
	host := func(routes map[string]string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			fmt.Fprintln(out, r.Method)
			mu.Unlock()
			if r.Method != "GET" {
				http.Error(w, "mutation refused", 405)
				return
			}
			s, ok := routes[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, s)
		}))
	}
	lemonade := host(map[string]string{"/api/v1/models": `{"data":[{"id":"Qwen3.6-35B-A3B-GGUF","downloaded":true}]}`, "/api/v1/health": `{"all_models_loaded":[{"model_name":"Qwen3.6-35B-A3B-GGUF","loaded":true}]}`})
	defer lemonade.Close()
	studio := host(map[string]string{"/api/v0/models": `{"data":[{"id":"qwen/qwen3.6-35b-a3b","type":"llm","state":"not-loaded"},{"id":"google/gemma-4-26b-it","type":"llm","state":"not-loaded"}]}`})
	defer studio.Close()
	var r *router.Router
	if *empty {
		r = router.New()
	} else {
		r = router.New(router.Lemonade(lemonade.URL), router.LMStudio(studio.URL), router.Ollama("http://127.0.0.1:1"))
	}
	r.Survey()
	h, err := service.Listen(*endpoint, r)
	if err != nil {
		panic(err)
	}
	defer h.Close()
	h.OnError = func(e error) { fmt.Fprintln(os.Stderr, e) }
	fmt.Println("router-v1: listening", *endpoint)
	if err = h.Serve(context.Background()); err != nil {
		panic(err)
	}
}
