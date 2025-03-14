# LokiLogger Package for writing logs to Loki

## Description

This package (`lokilogger`) provides a convenient mechanism for writing logs to Loki (Loki is a log aggregation system from Grafana Labs) using the standard log package. It allows grouping logs into packages (batching), adding metadata (service name), and automatically reconnecting to Loki when the connection is lost.

## Installation


go get github.com/LynxXIII/loki_logger

Example Usage

package main

import (
	"fmt"
	"os"

	lokilogger "github.com/LynxXIII/loki_logger"
)

func main() {
	cfg := lokilogger.Config{
		Name:       "Service Name",
		URL:        "http://loki:3100/loki/api/v1/batch", // Replace with your Loki URL
		BatchSize: 20,
		//AccessToken: "YOUR_LOKI_ACCESS_TOKEN", // Optional if you have an Access Token
	}

	loki, err := lokilogger.NewLokiWriter(cfg)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

    defer loki.Flush() //Sends log buffer before program exits (optional)

	log.Println("Starting service...")
	log.Println("This is a sample log message.")
	log.Println("Another log message with more details.")
}

Important Notes:

Replace "http://lokki:3100/lokki/api/v1/batch" with the actual URL of your Loki instance.
Comment out the AccessToken line if you're not using access tokens with Loki.  Access tokens are used for authentication.

Configuration Parameters (Config struct)

Name: The name of your service, which will be displayed in Loki.
URL: The URL of the Loki API endpoint for receiving logs.
BatchSize: The number of logs to collect into a single batch before sending. Optimize this value to achieve the best balance between latency and throughput.
AccessToken: An access token for authenticated access to Loki (optional).

Package Structure

lokilogs.go: Main package code.
lokilogs.go: Contains the Config struct and the NewLokkiWriter function.
lokilogs.go: Contains the getConn function for establishing a TCP connection with Loki.
lokilogs.go: Contains the isConnectionAlive function for checking the activity of the TCP connection.
lokilogs.go: Contains the prepareLogs and sendLogs methods for preparing and sending logs to Loki.
lokilogs.go: Implements the io.Writer interface to redirect log.Print to Loki.

Features

Batched Sending: Reduces load on the Loki server and improves efficiency.
Automatic Reconnect: Ensures continuous logging in case of connection breaks with Loki.
Access Token Support:  Allows for secure access to Loki.
Easy Integration: Simply replaces the standard log.Print, log.Println, log.Printf for convenient logging.

License
Apache License Version 2.0.