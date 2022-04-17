package polsvoice

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog/log"
)

type MentionHandler struct {
	ServerID     string
	OutDir       string
	StartRecChan chan RecordingRequest

	mu      sync.Mutex
	running *RecordingRequest
}

func NewMentionHandler(serverID, outDir string) *MentionHandler {
	return &MentionHandler{
		ServerID:     serverID,
		OutDir:       outDir,
		StartRecChan: make(chan RecordingRequest),
	}
}

type RecordingRequest struct {
	VoiceChannelID    string
	FilePrefix        string
	DisconnectionChan chan struct{}
	MessageChan       chan string
	ErrorChan         chan error
}

func (r *RecordingRequest) Println(s string) {
	r.MessageChan <- s
}

func (r *RecordingRequest) Printf(format string, a ...any) {
	r.Println(fmt.Sprintf(format, a...))
}

func (r *RecordingRequest) Complete(err error) {
	if err != nil {
		r.ErrorChan <- err
	}
	close(r.MessageChan)
	close(r.ErrorChan)
}

var recMsgRe = regexp.MustCompile("\\s+rec\\s+(.+)$")
var finishMsgRe = regexp.MustCompile("\\s+finish\\s+(.+)$")
var deleteMsgRe = regexp.MustCompile("\\s+delete$")

func (h *MentionHandler) Handle(s *discordgo.Session, m *discordgo.MessageCreate) {
	selfUserID := s.State.User.ID

	if m.Author.ID == selfUserID { // ignore the message comes from itself
		return
	}

	isMentioned := false
	for _, mention := range m.Mentions {
		if mention.ID == selfUserID {
			isMentioned = true
			break
		}
	}

	if !isMentioned {
		// do nothing when the message hasn't mentioned the bot
		return
	}

	if matchedIndex := recMsgRe.FindStringSubmatchIndex(m.Content); matchedIndex != nil {
		voiceChannelName := m.Content[matchedIndex[2]:matchedIndex[3]]
		go h.handleRecMessage(s, m, voiceChannelName)
		return
	}

	if matchedIndex := finishMsgRe.FindStringSubmatchIndex(m.Content); matchedIndex != nil {
		voiceChannelName := m.Content[matchedIndex[2]:matchedIndex[3]]
		go h.handleFinishMessage(s, m, voiceChannelName)
		return
	}

	if deleteMsgRe.MatchString(m.Content) {
		go h.handleDeleteMessage(s, m)
		return
	}

	h.handleUnknownMessage(s, m)
}

func (h *MentionHandler) handleRecMessage(s *discordgo.Session, m *discordgo.MessageCreate, voiceChannelName string) {
	voiceChannelID, err := h.getVoiceChannelID(s, voiceChannelName)
	if err != nil {
		log.Error().Err(err).Msg("failed to get channel list")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
		return
	}
	if voiceChannelID == "" {
		_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%q is not a voice channel, isn't it?", voiceChannelName))
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
		return
	}

	filePrefix, err := generateFilePrefix(m.Message, h.OutDir)
	if err != nil {
		log.Error().Err(err).Msg("failed to parse message id")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running == nil {
		req := RecordingRequest{
			VoiceChannelID:    voiceChannelID,
			FilePrefix:        filePrefix,
			DisconnectionChan: make(chan struct{}),
			MessageChan:       make(chan string),
			ErrorChan:         make(chan error),
		}
		h.running = &req

		h.StartRecChan <- req
		go func() {
			err := <-req.ErrorChan
			if aborted := err != nil; aborted {
				_, err = s.ChannelMessageSend(m.ChannelID, "Aborted...")
				if err != nil {
					log.Error().Err(err).Msg("failed to post to error msg")
				}

				h.running = nil
			}
		}()
		go func() {
			for {
				msg, ok := <-req.MessageChan
				if !ok {
					log.Debug().Msg("closed")
					return
				}

				_, err = s.ChannelMessageSend(m.ChannelID, msg)
				if err != nil {
					log.Error().Err(err).Msg("failed to post msg")
				}
			}
		}()

		_, err = s.ChannelMessageSend(m.ChannelID, "Okay, let's start recording")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
	} else {
		_, err := s.ChannelMessageSend(m.ChannelID, "already recording now")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
	}
}

func (h *MentionHandler) handleFinishMessage(s *discordgo.Session, m *discordgo.MessageCreate, voiceChannelName string) {
	voiceChannelID, err := h.getVoiceChannelID(s, voiceChannelName)
	if err != nil {
		log.Error().Err(err).Msg("failed to get channel list")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
		return
	}
	if voiceChannelID == "" {
		_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%q is not a voice channel, isn't it?", voiceChannelName))
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running == nil {
		_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%q is not recording now, isn't it?", voiceChannelName))
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
	} else {
		_, err = s.ChannelMessageSend(m.ChannelID, "Bye...")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to finish msg")
		}

		close(h.running.DisconnectionChan)
		h.running = nil
	}
}

func (h *MentionHandler) handleDeleteMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.MessageReference == nil {
		_, err := s.ChannelMessageSend(m.ChannelID, "please reply it for the target recoding request message.")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
		return
	}

	msg, err := s.ChannelMessage(m.MessageReference.ChannelID, m.MessageReference.MessageID)
	if err != nil {
		log.Error().Err(err).Msg("failed to get referenced message")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
		return
	}

	filePrefix, err := generateFilePrefix(msg, h.OutDir)
	if err != nil {
		log.Error().Err(err).Msg("failed to parse message id")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
		return
	}

	matches, err := filepath.Glob(filePrefix + "*.wav")
	if err != nil {
		log.Error().Err(err).Msg("failed to glob files")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
		return
	}
	if len(matches) == 0 {
		_, err := s.ChannelMessageSend(m.ChannelID, "no recerded files for the recording request.")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
		return
	}

	var mErr multiError
	for _, filePath := range matches {
		log.Debug().Msgf("remove %s", filePath)
		err := os.Remove(filePath)
		if err != nil {
			mErr = append(mErr, err)
		}
	}
	if len(mErr) == 0 {
		_, err := s.ChannelMessageSend(m.ChannelID, "successful to delete all recorded files.")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
	} else {
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to delete msg")
		}
	}
}

func (h *MentionHandler) Terminate() {
	close(h.StartRecChan)
	if h.running != nil {
		close(h.running.DisconnectionChan)
		h.running = nil
	}
}

func (h *MentionHandler) getVoiceChannelID(s *discordgo.Session, voiceChannelName string) (string, error) {
	channels, err := s.GuildChannels(h.ServerID)
	if err != nil {
		return "", err
	}

	for _, channel := range channels {
		if channel.Type == discordgo.ChannelTypeGuildVoice && channel.Name == voiceChannelName {
			return channel.ID, nil
		}
	}

	return "", nil
}

func (h *MentionHandler) handleUnknownMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	_, err := s.ChannelMessageSend(m.ChannelID, "I don't know what you mean.")
	if err != nil {
		log.Error().Err(err).Msg("failed to response to unknown msg")
	}
}

func generateFilePrefix(m *discordgo.Message, outDir string) (string, error) {
	msgID, err := strconv.ParseUint(m.ID, 10, 64)
	if err != nil {
		return "", err
	}

	msgTimestampMsecs := (msgID >> 22) + 1420070400000
	msgTimestamp := time.Unix(int64(msgTimestampMsecs/1000), int64((msgTimestampMsecs%1000)*1000*1000))
	return filepath.Join(outDir, m.Author.ID, msgTimestamp.Format("20060102"), m.ID), nil
}
