package main

import (
	"log"

	"github.com/SystemAlgoFund/chart_api/internal/config"
	"github.com/SystemAlgoFund/chart_api/internal/db"
	"github.com/SystemAlgoFund/chart_api/internal/server"
)

func main() {
	cfg, err := config.LoadConfig("config.yml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	db := db.NewDB(&cfg.DB)
	server.StartGRPCServer(cfg, db)
}
