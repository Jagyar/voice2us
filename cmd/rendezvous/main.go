// rendezvous — сервис-«встречалка» Voice2Us (см. internal/rdv).
//
//	rendezvous -addr :8787
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jagyar/voice2us/internal/rdv"
)

func main() {
	addr := flag.String("addr", ":8787", "адрес для прослушивания")
	flag.Parse()

	srv := rdv.NewServer()
	done := make(chan struct{})
	go srv.Run(done)

	hs := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
		close(done)
	}()

	log.Printf("rendezvous слушает %s", *addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
