// Command nectargate is the runnable entry point for the NectarGate intake
// backend. It seeds the rule catalog, opens the SQLite WAL store, wires the
// instrument adapters and the application service, and serves the HTTP API.
// On startup the store rebuilds its in-memory index and validates incomplete
// leases so the service recovers deterministically after a restart.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nectargate/raw-honey-maturation-intake/api"
	"nectargate/raw-honey-maturation-intake/catalog"
	"nectargate/raw-honey-maturation-intake/evidence"
	"nectargate/raw-honey-maturation-intake/service"
	"nectargate/raw-honey-maturation-intake/store"
)

func main() {
	addr := os.Getenv("NECTARGATE_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	dbPath := os.Getenv("NECTARGATE_DB")
	if dbPath == "" {
		dbPath = "nectargate.db"
	}

	cat := catalog.Seed(time.Now())

	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	svc := service.New(st, cat, nil, demoAdapters())
	srv := api.NewServer(svc)

	server := &http.Server{Addr: addr, Handler: srv.Handler()}

	go func() {
		log.Printf("nectargate listening on %s (db=%s)", addr, dbPath)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	log.Printf("nectargate stopped")
}

// demoAdapters wires deterministic in-memory instrument adapters so the
// instrument-triggered evidence path is reachable without real hardware.
func demoAdapters() map[evidence.InstrumentType]evidence.InstrumentAdapter {
	return map[evidence.InstrumentType]evidence.InstrumentAdapter{
		evidence.InstrumentQPCR:     evidence.NewScriptedAdapter(evidence.InstrumentQPCR, "30.00", ""),
		evidence.InstrumentSpectro:  evidence.NewScriptedAdapter(evidence.InstrumentSpectro, `{"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`, ""),
		evidence.InstrumentMoisture: evidence.NewScriptedAdapter(evidence.InstrumentMoisture, `{"hmf":"20.0","amylase":"12.0","moisture":"16.0","conductivity":"0.50","acidity":"30.0"}`, ""),
	}
}
