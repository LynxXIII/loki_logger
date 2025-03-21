package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	lokilogger "github.com/LynxXIII/loki_logger"
)

func handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fmt.Println("Ошибка при чтении тела запроса:", err)
		http.Error(w, "Ошибка сервера", http.StatusInternalServerError)
		return
	}

	fmt.Printf("Content:\n%s\n", body)
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second*10))
	defer cancel()

	http.HandleFunc("/loki/api/v1/push", handler)
	go func() {
		if err := http.ListenAndServe(":3100", nil); err != nil {
			log.Fatalln(err)
		}
	}()

	cfg := lokilogger.Config{
		Name:          "Service Name",
		URL:           "http://localhost:3100/loki/api/v1/push", // Replace with your Loki URL
		BatchSize:     2,
		FlushInterval: 5 * time.Second,
		RetryCount:    2,
		//AccessToken: "YOUR_LOKI_ACCESS_TOKEN", // Optional if you have an Access Token
	}

	if err := lokilogger.Init(ctx, cfg); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	log.Println("1. Starting service...")
	log.Println("2. This is a sample log message.")
	log.Println("3. This is a 'error' log message.")
	log.Println("4. ❌ Another log message.")
	log.Println("5. Another log message with more details.")

	<-ctx.Done()
	log.Println("Exit")
}
