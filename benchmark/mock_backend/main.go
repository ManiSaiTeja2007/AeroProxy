package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	serverName := os.Getenv("SERVER_NAME")
	if serverName == "" {
		serverName = "mock-backend"
	}

	http.HandleFunc("/delay", func(w http.ResponseWriter, r *http.Request) {
		msStr := r.URL.Query().Get("ms")
		ms, err := strconv.Atoi(msStr)
		if err != nil || ms < 0 {
			ms = 0
		}
		if ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		w.Header().Set("X-Backend-Name", serverName)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK from %s with delay %dms\n", serverName, ms)
	})

	http.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Name", serverName)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error from %s\n", serverName)
	})

	http.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Name", serverName)
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, r.Body)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fail-"+serverName) == "true" {
			w.Header().Set("X-Backend-Name", serverName)
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Failure induced on %s\n", serverName)
			return
		}
		delayHeader := r.Header.Get("X-Delay-" + serverName)
		if delayHeader != "" {
			if ms, err := strconv.Atoi(delayHeader); err == nil && ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}
		w.Header().Set("X-Backend-Name", serverName)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Hello from %s\n", serverName)
	})

	log.Printf("Starting mock backend %s on port %s...", serverName, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Mock backend failed: %v", err)
	}
}
