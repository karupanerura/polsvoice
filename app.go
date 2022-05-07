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

	mentionHandler := NewMentionHandler(serverID, outDir)
	removeMentionHandler := discord.AddHandler(mentionHandler.Handle)
	defer mentionHandler.Terminate()
	defer removeMentionHandler()

	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-mentionHandler.StartRecChan:
			vc, err := discord.ChannelVoiceJoin(serverID, req.VoiceChannelID, true, false)
			if err != nil {
				log.Error().Err(err).Msgf("failed to connect to voice channel %q", req.VoiceChannelID)
				req.Complete(err)
				continue
			}
			go func() {
				select {
				case <-ctx.Done():
					req.Println("force stopped!")
				case <-req.DisconnectionChan:
				}

				err := vc.Disconnect()
				if err != nil {
					log.Error().Err(err).Msg("failed to disconnect from voice channel")
				}
				close(vc.OpusRecv)
			}()

			log.Info().Msg("start recording...")
			recorder := NewRecorder(req.FilePrefix)
			err = recorder.Record(vc)
			if err != nil {
				log.Error().Err(err).Msgf("failed to recording: %v", err)
				req.Complete(err)
				continue
			}
			req.Println("finish recording!")

			req.Println("start finalizing...")
			mixer := NewAudioMixer(req.FilePrefix)
			err = mixer.Mixdown(ctx)
			if err != nil {
				log.Error().Err(err).Msgf("failed to mixdown: %v", err)
				req.Complete(err)
				continue
			}

			urlPathPrefix, err := filepath.Rel(outDir, req.FilePrefix)
			if err != nil {
				log.Error().Err(err).Msgf("failed to get relative path from %q to %q: %v", outDir, req.FilePrefix, err)
				req.Complete(err)
				continue
			}

			req.Println("complete! download from: https://team-nyanco.preview.karupas.org/polsvoice/" + urlPathPrefix + "-mix.wav")
			req.Complete(nil)
		}
	}
}
