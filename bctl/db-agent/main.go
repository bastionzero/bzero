package main

import (
	"os"

	"bastionzero.com/bctl/v1/bzerolib/logger"
)

// Declaring flags as package-accessible variables
// var (
// 	sessionId, authHeader, serviceUrl string
// 	daemonPort, logPath               string
// )

const (
	hubEndpoint = "/api/v1/hub/db/daemon"
	version     = "$DAEMON_VERSION"
)

func main() {
	// parseFlags()

	logger, err := logger.New(logger.Debug, "test.log")
	if err != nil {
		os.Exit(1)
	}
	logger.AddDaemonVersion(version)

	logger.Info("Hello world")

}

// func parseFlags() error {
// 	flag.StringVar(&sessionId, "sessionId", "", "Session ID From Zli")
// 	flag.StringVar(&authHeader, "authHeader", "", "Auth Header From Zli")

// 	// Our expected flags we need to start
// 	flag.StringVar(&serviceUrl, "serviceURL", "", "Service URL to use")

// 	// Plugin variables
// 	flag.StringVar(&daemonPort, "daemonPort", "", "Daemon Port To Use")
// 	flag.StringVar(&logPath, "logPath", "", "Path to log file for daemon")

// 	flag.Parse()

// 	// Check we have all required flags
// 	if sessionId == "" || authHeader == "" || serviceUrl == "" ||
// 		daemonPort == "" || logPath == "" {
// 		return fmt.Errorf("missing flags")
// 	}

// 	return nil
// }

// func getLogFilePath() string {
// 	return logPath
// }
