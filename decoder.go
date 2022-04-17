package polsvoice

import (
	"errors"
	"sort"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog/log"
	"layeh.com/gopus"
)

const packetBuffLen = 8

type DecodedDiscordPacket struct {
	*discordgo.Packet
	ReceivedAt time.Time
}

func ReceiveAndDecodePacket(vc *discordgo.VoiceConnection, ch chan DecodedDiscordPacket) error {
	if !vc.Ready {
		return errors.New("not yet ready")
	}
	if vc.OpusRecv == nil {
		return errors.New("no opus reciver")
	}

	state := discordPacketDecodeState{
		decoderMap:  map[uint32]*gopus.Decoder{},
		buffMap:     map[uint32][]DecodedDiscordPacket{},
		decodedChan: ch,
	}
	for p := range vc.OpusRecv {
		t := time.Now()
		if err := state.putPacket(p, t); err != nil {
			_ = state.reset()
			return err
		}
	}

	return state.reset()
}

type discordPacketDecodeState struct {
	decoderMap  map[uint32]*gopus.Decoder
	buffMap     map[uint32][]DecodedDiscordPacket
	decodedChan chan DecodedDiscordPacket
}

func (s *discordPacketDecodeState) putPacket(p *discordgo.Packet, t time.Time) error {
	buff, ok := s.buffMap[p.SSRC]
	if !ok {
		buff = make([]DecodedDiscordPacket, 0, packetBuffLen)
	}

	buff = append(buff, DecodedDiscordPacket{Packet: p, ReceivedAt: t.Add(-1 * time.Millisecond * time.Duration(len(p.PCM)) / 96)})
	s.buffMap[p.SSRC] = buff
	if len(buff) == cap(buff) {
		return s.flushBuffer(p.SSRC, len(buff)/2)
	}

	return nil
}

func (s *discordPacketDecodeState) getDecoder(ssrc uint32) (decoder *gopus.Decoder, err error) {
	var ok bool
	decoder, ok = s.decoderMap[ssrc]
	if !ok {
		decoder, err = gopus.NewDecoder(48000, 2)
		if err != nil {
			return
		}

		s.decoderMap[ssrc] = decoder
	}

	return
}

func (s *discordPacketDecodeState) flushBuffer(ssrc uint32, max int) error {
	log.Debug().Msgf("flush! SSRC:%d", ssrc)
	decoder, err := s.getDecoder(ssrc)
	if err != nil {
		return err
	}

	buff := s.buffMap[ssrc]
	sort.Slice(buff, func(i, j int) bool {
		return buff[i].Sequence < buff[j].Sequence
	})

	for _, p := range buff[:max] {
		p.PCM, err = decoder.Decode(p.Opus, 960, false)
		if err != nil {
			return err
		}

		s.decodedChan <- p
	}
	if max < len(buff) {
		copy(buff[:max], buff[max:])
	}
	s.buffMap[ssrc] = buff[:len(buff)-max]

	return nil
}

func (s *discordPacketDecodeState) reset() error {
	log.Debug().Msgf("reset!")
	var mErr multiError
	for ssrc, buff := range s.buffMap {
		err := s.flushBuffer(ssrc, len(buff))
		if err != nil {
			mErr = append(mErr, err)
		}

		if decoder := s.decoderMap[ssrc]; decoder != nil {
			decoder.ResetState()
		}
	}

	if len(mErr) != 0 {
		return mErr
	}
	return nil
}
