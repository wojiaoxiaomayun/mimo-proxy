package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/myapp/internal/keypool"
)

func main() {
	port := flag.Int("port", keypool.DefaultPort, "server port")
	targetURL := flag.String("target", keypool.DefaultTargetURL, "upstream API target URL")
	dbPath := flag.String("db", keypool.DefaultDBPath, "SQLite database file path")
	flag.Parse()

	pool, err := keypool.New(*dbPath, *targetURL)
	if err != nil {
		log.Fatalf("Failed to initialize key pool: %v", err)
	}
	defer pool.Close()

	if keysStr := os.Getenv("MIMO_API_KEYS"); keysStr != "" {
		if err := pool.LoadFromEnv(keysStr); err != nil {
			log.Printf("Failed to load keys from env: %v", err)
		}
	}

	mux := keypool.NewMux(pool, *targetURL)

	log.Printf("转发服务启动，监听 :%d，目标: %s，数据存储: %s", *port, *targetURL, *dbPath)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      mux,
		ReadTimeout:  keypool.ServerReadTimeout,
		WriteTimeout: keypool.ServerWriteTimeout,
		IdleTimeout:  keypool.ServerIdleTimeout,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
