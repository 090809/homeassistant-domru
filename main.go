package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/090809/homeassistant-domru/internal/controllers"
	"github.com/090809/homeassistant-domru/internal/domru"
	"github.com/090809/homeassistant-domru/internal/domru/constants"
	"github.com/090809/homeassistant-domru/internal/domru/sanitizing_utils"
	"github.com/090809/homeassistant-domru/internal/homeassistant"
	"github.com/090809/homeassistant-domru/pkg/auth"
	"github.com/090809/homeassistant-domru/pkg/authorizedhttp"
	"github.com/090809/homeassistant-domru/pkg/logging"
	"github.com/090809/homeassistant-domru/pkg/reverseproxy"
	"github.com/090809/homeassistant-domru/pkg/tokenmanagement"
)

//go:embed templates/*
var templateFs embed.FS

const (
	flagPort            = "port"
	flagRefreshToken    = "refresh-token"
	flagOperatorID      = "operator-id"
	flagCredentialsFile = "credentials"
	flagLogLevel        = "log-level"
	flagHaConfigFile    = "ha-config"
)

func initFlags() {
	pflag.Int(flagPort, 8080, "listen port")
	pflag.String(flagHaConfigFile, "/data/options.json", "home assistant config file")
	pflag.String(flagCredentialsFile, "/data/accounts.json", "credentials file path (i.e: /data/accounts.json")
	pflag.String(flagLogLevel, "info", "log level")
	pflag.String(flagRefreshToken, "", "refresh token")
	pflag.Int(flagOperatorID, 0, "operator id")
	pflag.Parse()

	err := viper.BindPFlags(pflag.CommandLine)
	if err != nil {
		log.Fatalf("Unable to bind flags: %v", err)
	}

	viper.SetConfigFile(viper.GetString(flagHaConfigFile))
	viper.SetConfigType("json")
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Printf("Error reading config file: %s", err)
		}
	}

	replacer := strings.NewReplacer("-", "_")
	viper.SetEnvKeyReplacer(replacer)
	viper.SetEnvPrefix("domru")
	viper.AutomaticEnv()
}

func initLogger() *slog.Logger {
	logLevel := logging.ParseLogLevel(viper.GetString(flagLogLevel))
	defaultHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel, AddSource: true})
	return slog.New(logging.NewSanitizingLoggerHandler(defaultHandler))
}

func main() {
	initFlags()

	logger := initLogger()

	listenAddr := fmt.Sprintf(":%d", viper.GetInt(flagPort))
	credentialsFile := viper.GetString(flagCredentialsFile)

	retryableClient := retryablehttp.NewClient()
	retryableClient.RetryMax = 5

	credentialsStore := auth.NewFileCredentialsStore(credentialsFile)

	overrideCredentialsWithFlags(credentialsStore, logger)

	authProvider := tokenmanagement.NewValidTokenProvider(credentialsStore)
	authProvider.Logger = logger
	authClient := authorizedhttp.NewClient(
		authProvider,
		authProvider,
		authProvider,
	)
	authClient.DefaultClient = retryableClient.StandardClient()
	authClient.Logger = logger

	domruAPI := domru.NewDomruAPI(authClient)
	domruAPI.Logger = logger

	haURL, err := homeassistant.GetHomeAssistantNetworkAddress()
	if err != nil {
		haURL = ""
	}

	mqttIntegration := homeassistant.NewMqttIntegration(
		domruAPI,
		logger,
		haURL,
	)
	go mqttIntegration.Start()

	handlers := controllers.NewHandlers(templateFs, credentialsStore, domruAPI)
	handlers.Logger = logger

	upstream, err := url.Parse(constants.BaseUrl)
	if err != nil {
		log.Fatal(err)
	}

	proxy := reverseproxy.NewReverseProxy(upstream)
	proxy.Client = authClient
	proxyHandler := proxy.ProxyRequestHandler()

	http.HandleFunc("GET /login", handlers.LoginPageHandler)
	http.HandleFunc("POST /login", handlers.LoginPhoneInputHandler)
	http.HandleFunc("GET /login/address", handlers.SelectAccountHandler)
	http.HandleFunc("POST /loginWithPassword", handlers.LoginWithPasswordHandler)
	http.HandleFunc("POST /sms", handlers.SubmitSmsCodeHandler)
	http.HandleFunc("GET /stream/{cameraId}", handlers.StreamController)
	http.HandleFunc("GET /pages/home.html", checkCredentialsMiddleware(credentialsStore, handlers.HomeHandler))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			logger.With("url", r.URL.String()).Debug("proxying request")
			proxyHandler(w, r)
		} else {
			logger.Debug("Redirecting to /pages/home.html")
			http.Redirect(w, r, "/pages/home.html", http.StatusMovedPermanently)
		}
	})

	log.Printf("Listening on %s\n", listenAddr)

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      nil,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  50 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Could not listen on %s: %v\n", listenAddr, err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("Shutting down server...")

	// Shutdown MQTT client
	mqttIntegration.Stop()

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server shutdown failed", "error", err)
	}

	logger.Info("Server gracefully stopped")
}

func overrideCredentialsWithFlags(credentialsStore *auth.FileCredentialsStore, logger *slog.Logger) {
	sanitizedToken := sanitizing_utils.KeepFirstNCharacters(viper.GetString(flagRefreshToken), 7)
	logger.With("refreshToken", sanitizedToken).With("operator-id", viper.GetInt(flagOperatorID)).Debug("Checking flags")
	if viper.GetString(flagRefreshToken) != "" && viper.GetInt(flagOperatorID) != 0 {
		logger.Info("Overriding credentials with flags")
		credentials := auth.Credentials{
			AccessToken:  "",
			RefreshToken: viper.GetString(flagRefreshToken),
			OperatorID:   viper.GetInt(flagOperatorID),
		}
		err := credentialsStore.SaveCredentials(credentials)
		if err != nil {
			logger.With("err", err.Error()).Error("Unable to save credentials")
		}
	}
}

func checkCredentialsMiddleware(credentialsStore auth.CredentialsStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		credentials, err := credentialsStore.LoadCredentials()
		if err != nil || credentials.RefreshToken == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	}
}
