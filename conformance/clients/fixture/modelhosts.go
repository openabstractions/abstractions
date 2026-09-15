package fixture

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

// ModelHosts serves the fake local model hosts every router proof reads: a
// Lemonade host with one downloaded, loaded model and an LM Studio host with
// two cold models. Only GET succeeds; onRequest, when set, observes each
// method so a proof can show the router never mutated a host. Callers build
// their router from the returned URLs and add router.Ollama("http://127.0.0.1:1")
// as the unreachable third host. close stops both servers.
func ModelHosts(onRequest func(method string)) (lemonade, lmstudio string, close func()) {
	host := func(routes map[string]string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if onRequest != nil {
				onRequest(r.Method)
			}
			if r.Method != http.MethodGet {
				http.Error(w, "mutation refused", http.StatusMethodNotAllowed)
				return
			}
			body, ok := routes[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, body)
		}))
	}
	l := host(map[string]string{
		"/api/v1/models": `{"data":[{"id":"Qwen3.6-35B-A3B-GGUF","downloaded":true}]}`,
		"/api/v1/health": `{"all_models_loaded":[{"model_name":"Qwen3.6-35B-A3B-GGUF","loaded":true}]}`,
	})
	s := host(map[string]string{
		"/api/v0/models": `{"data":[{"id":"qwen/qwen3.6-35b-a3b","type":"llm","state":"not-loaded"},{"id":"google/gemma-4-26b-it","type":"llm","state":"not-loaded"}]}`,
	})
	return l.URL, s.URL, func() { l.Close(); s.Close() }
}

// LiveModel is the model the fake hosts carry resident on Lemonade and cold on LM Studio.
const LiveModel = "qwen/qwen3.6-35b-a3b"

// UnreachableOllama is the third router host, reported down with a reason.
const UnreachableOllama = "http://127.0.0.1:1"
