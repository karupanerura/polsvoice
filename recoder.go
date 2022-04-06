package polsvoice

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/bwmarrin/dgvoice"
	"github.com/bwmarrin/discordgo"
	"github.com/karupanerura/wavebin"
	"github.com/rs/zerolog/log"
)

const numOfAudioChannels = wavebin.StereoChannels
const samplingRate = 48000
const samplingBit = 16

type Recorder struct {
	filePrefix   string
	withSplitted bool
}

func NewRecorder(filePrefix string, withSplitted bool) *Recorder {
	return &Recorder{
		filePrefix:   filePrefix,
		withSplitted: withSplitted,
	}
}

func (r *Recorder) Record(ctx context.Context, vc *discordgo.VoiceConnection) error {
	rx := make(chan *discordgo.Packet, 2)

	mixedRecorder, err := newFileRecorder(r.filePrefix + ".wav")
	if err != nil {
		log.Error().Err(err).Msgf("failed to open file %q", r.filePrefix+".wav")
	}

	var splitedRecorder recorder = nopRecorder{}
	if r.withSplitted {
		splitedRecorder = newSplittedRecorder(r.filePrefix)
	}

	go dgvoice.ReceivePCM(vc, rx)

	var p *discordgo.Packet
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("finalizing...")

			err := mixedRecorder.Close()
			if err != nil {
				log.Error().Err(err).Msg("failed to finalize PCM recording")
			}

			err = splitedRecorder.Close()
			if err != nil {
				log.Error().Err(err).Msg("failed to finalize PCM recording")
			}

			// MEMO should flush all remained packets in rx channel?

			return nil
		case p = <-rx:
			err := mixedRecorder.recordFromPacket(p)
			if err != nil {
				log.Error().Err(err).Msg("failed to write PCM")
			}

			err = splitedRecorder.recordFromPacket(p)
			if err != nil {
				log.Error().Err(err).Msg("failed to write PCM")
			}
		}
	}
}

type recorder interface {
	recordFromPacket(p *discordgo.Packet) error
	io.Closer
}

type nopRecorder struct{}

func (r nopRecorder) recordFromPacket(p *discordgo.Packet) error {
	return nil
}

func (r nopRecorder) Close() error {
	return nil
}

type splittedRecorder struct {
	filePrefix string
	m          map[uint32]recorder
}

func newSplittedRecorder(filePrefix string) *splittedRecorder {
	return &splittedRecorder{
		filePrefix: filePrefix,
		m:          map[uint32]recorder{},
	}
}

func (r *splittedRecorder) recordFromPacket(p *discordgo.Packet) error {
	if rec, ok := r.m[p.SSRC]; ok {
		return rec.recordFromPacket(p)
	} else {
		fileName := fmt.Sprintf("%s-%d.wav", r.filePrefix, p.SSRC)
		rec, err := newFileRecorder(fileName)
		if err != nil {
			return err
		}

		r.m[p.SSRC] = rec
		return rec.recordFromPacket(p)
	}
}

func (r *splittedRecorder) Close() error {
	var mErr multiError
	for _, rr := range r.m {
		err := rr.Close()
		if err != nil {
			mErr = append(mErr, err)
		}
	}

	if len(mErr) != 0 {
		return mErr
	}
	return nil
}

type multiError []error

func (e multiError) Error() string {
	if len(e) == 1 {
		return e.Error()
	}

	s := strings.Builder{}
	s.WriteString("multi errors: ")
	for i, ee := range e {
		if i != 0 {
			s.WriteString(", ")
		}
		s.WriteString(ee.Error())
	}

	return s.String()
}

const packetBuffLen = 256

type fileRecorder struct {
	f            *os.File
	sampleWriter io.WriteCloser
	bufioWriter  *bufio.Writer
	buff         []*discordgo.Packet
}

func newFileRecorder(fileName string) (*fileRecorder, error) {
	f, err := os.Create(fileName)
	if err != nil {
		return nil, err
	}

	w, err := wavebin.CreateSampleWriter(f, &wavebin.ExtendedFormatChunk{
		MetaFormat: wavebin.NewPCMMetaFormat(wavebin.StereoChannels, samplingRate, samplingBit),
	})
	if err != nil {
		return nil, err
	}

	return &fileRecorder{
		f:            f,
		sampleWriter: w,
		bufioWriter:  bufio.NewWriter(w),
		buff:         make([]*discordgo.Packet, 0, packetBuffLen),
	}, nil
}

func (r *fileRecorder) recordFromPacket(p *discordgo.Packet) error {
	r.buff = append(r.buff, p)
	if len(r.buff) >= packetBuffLen {
		return r.flushBuffer(len(r.buff) / 2)
	}

	return nil
}

func (r *fileRecorder) flushBuffer(max int) error {
	sort.Slice(r.buff, func(i, j int) bool {
		return r.buff[i].Sequence < r.buff[j].Sequence
	})

	pcmWriter := wavebin.PCMWriter{W: r.bufioWriter}
	for _, p := range r.buff[:max] {
		pcmLen := len(p.PCM)
		for i := 0; i < pcmLen; i += 2 {
			sample := wavebin.PCM16BitStereoSample{
				L: p.PCM[i],
				R: p.PCM[i+1],
			}

			_, err := pcmWriter.WriteSamples(&sample)
			if err != nil {
				return err
			}
		}
	}
	if max < len(r.buff) {
		copy(r.buff[:max], r.buff[max:])
	}
	r.buff = r.buff[:max]

	return nil
}

func (r *fileRecorder) Close() error {
	err := r.flushBuffer(len(r.buff))
	if err != nil {
		_ = r.sampleWriter.Close()
		_ = r.f.Close()
		return err
	}

	err = r.bufioWriter.Flush()
	if err != nil {
		_ = r.sampleWriter.Close()
		_ = r.f.Close()
		return err
	}

	err = r.sampleWriter.Close()
	if err != nil {
		_ = r.f.Close()
		return err
	}

	return r.f.Close()
}

func mutedPacket(src *discordgo.Packet) *discordgo.Packet {
	dst := &discordgo.Packet{
		SSRC:      src.SSRC,
		Sequence:  src.Sequence,
		Timestamp: src.Timestamp,
		Type:      make([]byte, len(src.Type)),
		Opus:      make([]byte, len(src.Opus)),
		PCM:       make([]int16, len(src.PCM)),
	}
	copy(dst.Type, src.Type) // keep content
	copy(dst.Opus, src.Opus) // keep content
	return dst
}
