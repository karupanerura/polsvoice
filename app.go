package polsvoice

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog/log"
)

// Run is an entry point of the app.
func Run(ctx context.Context, botToken string, serverID string, channelID string, filePrefix string, withSplitted bool) error {
	discord, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return err
	}

	err = discord.Open()
	if err != nil {
		return err
	}
	defer func() {
		err := discord.Close()
		if err != nil {
			log.Error().Err(err).Msg("failed to close discord instance")
		}
	}()

	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	defer cancel()

	startRecChan := make(chan struct{})
	disconnectionChan := make(chan struct{})
	mentionHandler := &MentionHandler{
		StartRecChan:      startRecChan,
		DisconnectionChan: disconnectionChan,
	}
	removeMentionHandler := discord.AddHandler(mentionHandler.Handle)
	defer close(startRecChan)
	defer close(disconnectionChan)
	defer removeMentionHandler()
	ctx = contextWithSignal(ctx, disconnectionChan)

	log.Info().Msg("standby recording in speaker mute")
	initialVC, err := discord.ChannelVoiceJoin(serverID, channelID, true, true)
	if err != nil {
		return err
	}
	defer func() {
		if initialVC == nil {
			return
		}

		err := initialVC.Disconnect()
		if err != nil {
			log.Error().Err(err).Msg("failed to disconnect from voice channel in mute (i.e. initial state)")
		}
	}()

	select {
	case <-ctx.Done():
		log.Info().Msg("finished app")
		return nil
	case <-startRecChan:
		// fall through
	}

	// reconnect to listen the voice channel
	err = initialVC.Disconnect()
	if err != nil {
		return err
	}
	initialVC = nil // mark as disconnected

	vc, err := discord.ChannelVoiceJoin(serverID, channelID, true, false)
	if err != nil {
		return err
	}
	defer func() {
		err = vc.Disconnect()
		if err != nil {
			log.Error().Err(err).Msg("failed to disconnect from voice channel")
		}
	}()

	log.Info().Msg("start recording...")
	recorder := NewRecorder(filePrefix, withSplitted)
	err = recorder.Record(ctx, vc)
	if err != nil {
		return err
	}

	return nil
}

func contextWithSignal(ctx context.Context, ch chan struct{}) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-ch:
		case <-ctx.Done():
		}
		cancel()
	}()
	return ctx
}
