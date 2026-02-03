package main

import (
	"log"
	"net/http"
	"os"

	"github.com/cloudfoundry/seaweedfs-broker/broker"
	"github.com/cloudfoundry/seaweedfs-broker/config"
)

func main() {
	configPath := os.Getenv("BROKER_CONFIG_PATH")
	if configPath == "" {
		configPath = "/var/vcap/jobs/seaweedfs-broker/config/broker.yml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	b, err := broker.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create broker: %v", err)
	}

	router := b.Router()

	addr := cfg.ListenAddr
	if addr == "" {
		addr = ":8080"
	}

	log.Printf("SeaweedFS Service Broker starting on %s (config schema: %s)", addr, config.ConfigVersion)
	log.Printf("Catalog: %d services available", len(cfg.Catalog.Services))

	server := &http.Server{
		Addr:    addr,
		Handler: router,
	}

	if cfg.TLS.Enabled {
		log.Printf("TLS enabled, using cert: %s", cfg.TLS.CertFile)
		err = server.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	} else {
		err = server.ListenAndServe()
	}

	if err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
