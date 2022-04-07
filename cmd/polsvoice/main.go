package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/moznion/polsvoice"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	var botToken string
	var serverID string
	var outDir string

	flag.StringVar(&botToken, "bot-token", "", "[mandatory] secret token of the Discord bot")
	flag.StringVar(&serverID, "server-id", "", "[mandatory] server ID to join in")
	flag.StringVar(&outDir, "out-dir", "", "[mandatory] output directory")

	flag.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, `polsvoice: A Discord bot to record the sound in voice chat.

Usage of %s:
   %s [OPTIONS]
Options
`, os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if botToken == "" {
		flag.Usage()
		log.Fatal().Msg("--bot-token is a mandatory parameter")
	}
	if serverID == "" {
		flag.Usage()
		log.Fatal().Msg("--server-id is a mandatory parameter")
	}
	if outDir == "" {
		flag.Usage()
		log.Fatal().Msg("--out-dir is a mandatory parameter")
	}

	// verify dir
	if s, err := os.Stat(outDir); os.IsNotExist(err) {
		log.Fatal().Msg("--out-dir is not exists")
	} else if !s.IsDir() {
		log.Fatal().Msg("--out-dir is not a directory")
	}

	err := polsvoice.Run(context.Background(), botToken, serverID, outDir)
	if err != nil {
		log.Fatal().Err(err).Send()
	}
}
