package main

import (
	"log"
	"net/http"
	"os"

	"github.com/myapp/internal/keypool"
)

const (
	targetURL = "https://api.xiaomimimo.com/v1/chat/completions"
	port     = "8080"
	dbPath   = "keys.db"
)

func main() {
	pool, err := keypool.New(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize key pool: %v", err)
	}
	defer pool.Close()

	if keysStr := os.Getenv("MIMO_API_KEYS"); keysStr != "" {
		if err := pool.LoadFromEnv(keysStr); err != nil {
			log.Printf("Failed to load keys from env: %v", err)
		}
	}

	mux := keypool.NewMux(pool, targetURL)

	log.Printf("转发服务启动，监听 :%s，数据存储: %s", port, dbPath)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}