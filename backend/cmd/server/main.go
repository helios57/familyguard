// Command server is the Family Guard control plane: the API the DPC talks to, the console the
// parents use, and the migrations that create the schema it all rests on — one binary, one image.
//
// Startup is ordered so that every way this deployment could be wrong is found before the port is
// bound: configuration first, then the database and its migrations, then the identity provider's
// keys, then the APK checksums. A server that has begun accepting requests has already proven it can
// do the things it will be asked to do.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/helios57/familyguard/backend/internal/auth"
	"github.com/helios57/familyguard/backend/internal/config"
	"github.com/helios57/familyguard/backend/internal/httpapi"
	"github.com/helios57/familyguard/backend/internal/provisioning"
	"github.com/helios57/familyguard/backend/internal/store"
)

// shutdownGrace is how long in-flight requests get once a signal arrives. It is longer than a
// request and shorter than Kubernetes' default terminationGracePeriodSeconds of 30s, so the process
// finishes on its own terms rather than being killed mid-response.
const shutdownGrace = 20 * time.Second

// version is stamped in at link time (`-X main.version=…`, see backend/Dockerfile). It is logged
// once at startup so a running pod can say what it is; it is not an authority on what was deployed.
// The digest is — a tag can be moved, and this string is only as honest as whoever passed it.
var version = "dev"

func main() {
	if err := run(); err != nil {
		// Written to stderr as well as the logger, because the most likely failure here is a
		// configuration error thrown before the logger's level is even known.
		fmt.Fprintln(os.Stderr, "fatal: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	// The signal context covers startup too. A pod killed while waiting on a database that will
	// never come up should exit, not sit in the retry loop until the grace period runs out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Bootstrap is idempotent and seeds parents only into a family that has none, so it runs on
	// every start and can never resurrect a parent the primary admin removed.
	fam, err := st.Bootstrap(ctx, cfg.FamilyName, cfg.BootstrapEmail)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	log.Info("family ready", "family_id", fam.ID, "name", fam.Name)

	parents, err := st.ListParents(ctx)
	if err != nil {
		return fmt.Errorf("list parents: %w", err)
	}
	if len(parents) == 0 {
		// Not a warning. Parent sign-in is the only way into this server and there is no
		// self-service registration, so with no parent row nobody can ever sign in — the console
		// would come up, accept a Google login, and refuse every single one.
		return errors.New("no parents exist and BOOTSTRAP_PARENT_EMAILS is empty: " +
			"nobody would be able to sign in")
	}

	verifier, err := auth.NewOIDCVerifier(cfg.OIDCIssuer, cfg.OIDCJWKSURL, cfg.OAuthClientID, nil)
	if err != nil {
		return fmt.Errorf("oidc verifier: %w", err)
	}
	sessions, err := auth.NewSessionIssuer(cfg.SessionKey, cfg.SessionTTL)
	if err != nil {
		return fmt.Errorf("session issuer: %w", err)
	}

	signatureSum, packageSum, err := apkChecksums(cfg)
	if err != nil {
		return err
	}
	// Which DPC is this process vending? Until this line existed, nothing answered that. The APK is
	// deliberately not in the image — it is a file on the node, installed out of band — so the only
	// record of which build a running server publishes checksums for was the checksums themselves,
	// held in memory and printed into QR codes nobody reads back. Neither value is a secret: the
	// signature checksum is in every provisioning payload, and the package checksum is the SHA-256
	// of a file this server hands to anyone who asks for /dpc.apk. An empty value here is the
	// honest report that provisioning cannot be offered, not a formatting accident.
	log.Info("provisioning checksums computed from disk",
		"apk_path", cfg.APKPath, "cert_path", cfg.APKCertPath,
		"package_checksum", packageSum, "signature_checksum", signatureSum)

	srv, err := httpapi.New(httpapi.Deps{
		Config:            cfg,
		Store:             st,
		Verifier:          verifier,
		Sessions:          sessions,
		Logger:            log,
		SignatureChecksum: signatureSum,
		PackageChecksum:   packageSum,
	})
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	router, err := srv.Router()
	if err != nil {
		return fmt.Errorf("router: %w", err)
	}

	// Bind before announcing. ListenAndServe would let the "listening" line print and then fail on
	// a port already in use, so the log would claim a success nothing had checked — and the process
	// would exit a moment later with the two lines in the wrong order. Listening explicitly also
	// resolves the address, so ADDR=127.0.0.1:0 logs the port it actually got.
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	httpServer := &http.Server{
		Handler: router,
		// ReadHeaderTimeout bounds a slowloris; ReadTimeout bounds a slow body. There is
		// deliberately NO WriteTimeout: it applies to the whole response, and the parent event
		// stream is a response that stays open for fifteen minutes by design. A WriteTimeout here
		// would sever every SSE connection on a fixed schedule and look like a network fault.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	go maintain(ctx, st, cfg, log)

	log.Info("listening",
		"addr", listener.Addr().String(), "public_url", cfg.PublicURL.String(), "version", version)

	errc := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// The shutdown context is deliberately not derived from ctx: ctx is already cancelled by the
	// signal, and a cancelled parent would make Shutdown return immediately without draining
	// anything — the graceful path that silently is not.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	// Closing the hub first ends the open event streams, so their handlers return and Shutdown has
	// something finite to wait for. Left open, every connected console would hold shutdown until
	// the grace period expired and the process was killed.
	srv.Hub().Close()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// apkChecksums computes what goes into a provisioning QR, from bytes on disk.
//
// Neither value is ever a configured constant: a checksum that is typed rather than computed is a
// checksum that verifies whatever it was typed from, which after the next rebuild is nothing.
func apkChecksums(cfg *config.Config) (signature, pkg string, err error) {
	if cfg.APKCertPath != "" {
		if signature, err = provisioning.ChecksumFile(cfg.APKCertPath); err != nil {
			return "", "", fmt.Errorf("apk signing certificate: %w", err)
		}
	}
	if cfg.APKPath != "" {
		if pkg, err = provisioning.ChecksumFile(cfg.APKPath); err != nil {
			return "", "", fmt.Errorf("apk: %w", err)
		}
	}
	return signature, pkg, nil
}

// maintain expires commands and drops telemetry past its retention window.
//
// It runs in this process rather than as a CronJob because it is one query per table and because a
// second workload would need its own database credential. It logs the row counts it actually
// deleted: "maintenance ran" with no numbers is indistinguishable from maintenance that matched
// nothing because its predicate was wrong.
func maintain(ctx context.Context, st *store.Store, cfg *config.Config, log *slog.Logger) {
	ticker := time.NewTicker(cfg.MaintenanceInterval)
	defer ticker.Stop()

	sweep := func() {
		opCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()

		expired, err := st.ExpireCommands(opCtx)
		if err != nil {
			log.Error("maintenance: expire commands", "error", err)
		}
		audit, err := st.PruneAudit(opCtx, cfg.AuditRetentionDays)
		if err != nil {
			log.Error("maintenance: prune audit", "error", err)
		}
		locations, err := st.PruneLocations(opCtx, cfg.LocationRetention)
		if err != nil {
			log.Error("maintenance: prune locations", "error", err)
		}
		log.Info("maintenance",
			"commands_expired", expired, "audit_pruned", audit, "locations_pruned", locations)
	}

	// Once at startup, so a pod that restarts more often than the interval still sweeps.
	sweep()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	// JSON to stdout: the cluster's log pipeline parses it, and a human reading `kubectl logs` gets
	// one line per event rather than a wrapped multi-line format that no grep can match.
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
