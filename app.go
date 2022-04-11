package polsvoice

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog/log"
)

// Run is an entry point of the app.
func Run(ctx context.Context, botToken string, serverID string, outDir string) error {
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

	mentionHandler := NewMentionHandler(serverID)
	removeMentionHandler := discord.AddHandler(mentionHandler.Handle)
	defer mentionHandler.Terminate()
	defer removeMentionHandler()

	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-mentionHandler.StartRecChan:
			func(req RecordingRequest) {
				ctx := contextWithSignal(ctx, req.DisconnectionChan)

				vc, err := discord.ChannelVoiceJoin(serverID, req.VoiceChannelID, true, false)
				if err != nil {
					log.Error().Err(err).Msgf("failed to connect to voice channel %q", req.VoiceChannelID)
					req.Complete(err)
					return
				}
				defer func() {
					err := vc.Disconnect()
					if err != nil {
						log.Error().Err(err).Msg("failed to disconnect from voice channel")
					}
				}()

				log.Info().Msg("start recording...")
				recorder := NewRecorder(filepath.Join(outDir, req.FilePrefix))
				err = recorder.Record(ctx, vc)
				if err != nil {
					log.Error().Err(err).Msgf("failed to recording: %v", err)
					req.Complete(err)
					return
				}
				req.Println("finish recording!")

				req.Println("start finalizing...")
				mixer := NewAudioMixer(filepath.Join(outDir, req.FilePrefix))
				err = mixer.Mixdown(ctx)
				if err != nil {
					log.Error().Err(err).Msgf("failed to mixdonw: %v", err)
					req.Complete(err)
					return
				}

				req.Println("complete! download from: https://team-nyanco.preview.karupas.org/polsvoice/"+req.FilePrefix+"-mix.wav")
				req.Complete(nil)
			}(req)
		}
	}
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
