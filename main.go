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
	port := flag.Int("port", 10081, "server port")
	flag.Parse()

	targetURL := "https://api.xiaomimimo.com/v1/chat/completions"
	dbPath := "keys.db"

	pool, err := keypool.New(dbPath, targetURL)
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

	log.Printf("转发服务启动，监听 :%d，数据存储: %s", *port, dbPath)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), mux); err != nil {
		log.Fatal(err)
	}
}
