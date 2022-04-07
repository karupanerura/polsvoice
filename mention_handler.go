package polsvoice

import (
	"fmt"
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
	StartRecChan chan RecordingRequest

	mu      sync.Mutex
	running *RecordingRequest
}

func NewMentionHandler(serverID string) *MentionHandler {
	return &MentionHandler{
		ServerID:     serverID,
		StartRecChan: make(chan RecordingRequest),
	}
}

type RecordingRequest struct {
	VoiceChannelID    string
	FilePrefix        string
	DisconnectionChan chan struct{}
	ErrorChan         chan error
}

func (r *RecordingRequest) Complete(err error) {
	if err != nil {
		r.ErrorChan <- err
	}
	close(r.ErrorChan)
}

var recMsgRe = regexp.MustCompile("\\s+rec\\s+(.+)$")
var finishMsgRe = regexp.MustCompile("\\s+finish\\s+(.+)$")

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
	}
	if voiceChannelID == "" {
		_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%q is not a voice channel, isn't it?", voiceChannelName))
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
	}

	msgID, err := strconv.ParseUint(m.ID, 10, 64)
	if err != nil {
		log.Error().Err(err).Msg("failed to get channel list")
		_, err := s.ChannelMessageSend(m.ChannelID, "Oops! Please retry")
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running == nil {
		msgTimestampMsecs := (msgID >> 22) + 1420070400000
		msgTimestamp := time.Unix(int64(msgTimestampMsecs/1000), int64((msgTimestampMsecs%1000)*1000*1000))
		req := RecordingRequest{
			VoiceChannelID:    voiceChannelID,
			FilePrefix:        filepath.Join(m.Author.ID, msgTimestamp.Format("20060102"), m.ID),
			DisconnectionChan: make(chan struct{}),
			ErrorChan:         make(chan error),
		}
		h.running = &req

		h.StartRecChan <- req
		go func() {
			err := <-req.ErrorChan
			if aborted := err != nil; aborted {
				_, err = s.ChannelMessageSend(m.ChannelID, "Aborted...")
				if err != nil {
					log.Error().Err(err).Msg("failed to response to rec msg")
				}

				h.running = nil
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
	}
	if voiceChannelID == "" {
		_, err := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%q is not a voice channel, isn't it?", voiceChannelName))
		if err != nil {
			log.Error().Err(err).Msg("failed to response to rec msg")
		}
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
