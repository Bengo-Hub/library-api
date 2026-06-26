package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/bengobox/library-service/internal/app"
)

// @title Library Service API
// @version 0.1.0
// @description HTTP API for the Codevertex library management service. Catalog, circulation, members, fines and e-books.
// @BasePath /api/v1
// @schemes http https
// @securityDefinitions.apikey bearerAuth
// @in header
// @name Authorization
// @description JWT token from auth-service. Format: Bearer {token}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx)
	if err != nil {
		log.Fatalf("failed to initialise app: %v", err)
	}
	defer a.Close()

	if err := a.Run(ctx); err != nil {
		log.Fatalf("runtime error: %v", err)
	}
}
