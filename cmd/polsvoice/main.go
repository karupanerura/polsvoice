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
	var channelID string
	var filePrefix string
	var withSplitted bool

	flag.StringVar(&botToken, "bot-token", "", "[mandatory] secret token of the Discord bot")
	flag.StringVar(&serverID, "server-id", "", "[mandatory] server ID to join in")
	flag.StringVar(&channelID, "channel-id", "", "[mandatory] voice channel ID to join in")
	flag.StringVar(&filePrefix, "file-prefix", "", "[mandatory] output file prefix. if this value is \"test\", this will make \"test-mix.wav\". and if \"--with-spliited\" is flagged, this will make \"test-${SSRC}.wav\" too.")
	flag.BoolVar(&withSplitted, "with-splitted", false, "record the sound splitted by the speakers")

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
	if channelID == "" {
		flag.Usage()
		log.Fatal().Msg("--channel-id is a mandatory parameter")
	}
	if filePrefix == "" {
		log.Fatal().Msg("--file-prefix is a mandatory parameter")
	}

	err := polsvoice.Run(context.Background(), botToken, serverID, channelID, filePrefix, withSplitted)
	if err != nil {
		log.Fatal().Err(err).Send()
	}
}
