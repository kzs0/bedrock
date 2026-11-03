package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"

	_ "github.com/kzs0/bedrock"
)

func main() {
	statuses := make(map[string]int)
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/goroutine"} {
		w := httptest.NewRecorder()
		http.DefaultServeMux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		statuses[path] = w.Code
	}
	if err := json.NewEncoder(os.Stdout).Encode(statuses); err != nil {
		panic(err)
	}
}
