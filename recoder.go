package polsvoice

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/karupanerura/wavebin"
	"github.com/rs/zerolog/log"
)

var pcmWaveMetaFormat = wavebin.NewPCMMetaFormat(wavebin.StereoChannels, 48000, 16)

type Recorder struct {
	filePrefix string
}

func NewRecorder(filePrefix string) *Recorder {
	return &Recorder{
		filePrefix: filePrefix,
	}
}

func (r *Recorder) prepareDir() error {
	if os.IsPathSeparator(r.filePrefix[len(r.filePrefix)-1]) {
		return os.MkdirAll(r.filePrefix, os.ModeDir|0o755)
	}

	return os.MkdirAll(filepath.Dir(r.filePrefix), os.ModeDir|0o755)
}

func (r *Recorder) Record(vc *discordgo.VoiceConnection) error {
	if err := r.prepareDir(); err != nil {
		return err
	}

	separatedRecorder := newSeparatedRecorder(r.filePrefix)

	rx := make(chan DecodedDiscordPacket, 2)
	go func() {
		err := ReceiveAndDecodePacket(vc, rx)
		if err != nil {
			log.Error().Err(err).Msg("failed to receive packet")
		}
		close(rx)
	}()

	var p DecodedDiscordPacket
	var err error
	for p = range rx {
		err = separatedRecorder.recordFromPacket(p)
		if err != nil {
			log.Error().Err(err).Msg("failed to write PCM")
		}
	}

	log.Info().Msg("finalizing...")

	err = separatedRecorder.Close()
	if err != nil {
		log.Error().Err(err).Msg("failed to finalize PCM recording")
	}

	return err
}

type separatedRecorder struct {
	filePrefix     string
	fileRecorders  map[uint32]*fileRecorder
	lastTimestamps map[uint32]uint32
	firstReceivedAt time.Time
}

func newSeparatedRecorder(filePrefix string) *separatedRecorder {
	return &separatedRecorder{
		filePrefix:     filePrefix,
		fileRecorders:  map[uint32]*fileRecorder{},
		lastTimestamps: map[uint32]uint32{},
	}
}

func (r *separatedRecorder) recordFromPacket(p DecodedDiscordPacket) error {
	if rec, ok := r.fileRecorders[p.SSRC]; ok {
		if lastTimestamp := r.lastTimestamps[p.SSRC]; lastTimestamp < p.Timestamp {
			samples := p.Timestamp - lastTimestamp
			if err := rec.insertSilentSamples(samples); err != nil {
				return err
			}
		}
		r.lastTimestamps[p.SSRC] = p.Timestamp + uint32(len(p.PCM) / 2)

		if err := rec.recordRawPCM(p.PCM); err != nil {
			return err
		}
	} else {
		fileName := fmt.Sprintf("%s-%d.wav", r.filePrefix, p.SSRC)
		rec, err := newFileRecorder(fileName)
		if err != nil {
			return err
		}

		r.fileRecorders[p.SSRC] = rec
		r.lastTimestamps[p.SSRC] = p.Timestamp + uint32(len(p.PCM)/2)

		log.Debug().Msgf("First Timestamp[%d]: %d (samples:%d)", p.SSRC, p.Timestamp, len(p.PCM))
		log.Debug().Msgf("First Sequence[%d]: %d", p.SSRC, p.Sequence)
		log.Debug().Msgf("First ReceivedAt: %v", r.firstReceivedAt)
		log.Debug().Msgf("Current ReceivedAt: %v", p.ReceivedAt)
		log.Debug().Msgf("ReceivedAt Diff: %v", p.ReceivedAt.Sub(r.firstReceivedAt))

		// insert silent samples for first
		if r.firstReceivedAt.IsZero() {
			r.firstReceivedAt = p.ReceivedAt
		} else if r.firstReceivedAt.Before(p.ReceivedAt) {
			msec := p.ReceivedAt.Sub(r.firstReceivedAt).Milliseconds()
			samples := uint32(msec*48)
			if err := rec.insertSilentSamples(samples); err != nil {
				return err
			}
		}

		if err = rec.recordRawPCM(p.PCM); err != nil {
			return err
		}
	}

	return nil
}

func (r *separatedRecorder) Close() error {
	var mErr multiError
	for _, rr := range r.fileRecorders {
		if err := rr.Close(); err != nil {
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

type fileRecorder struct {
	f            *os.File
	sampleWriter io.WriteCloser
	buffWriter   *bufio.Writer
}

func newFileRecorder(fileName string) (*fileRecorder, error) {
	f, err := os.Create(fileName)
	if err != nil {
		return nil, err
	}

	w, err := wavebin.CreateSampleWriter(f, &wavebin.ExtendedFormatChunk{
		MetaFormat: pcmWaveMetaFormat,
	})
	if err != nil {
		return nil, err
	}

	return &fileRecorder{
		f:            f,
		sampleWriter: w,
		buffWriter:   bufio.NewWriter(w),
	}, nil
}

func (r *fileRecorder) recordRawPCM(samples []int16) error {
	pcmWriter := wavebin.PCMWriter[wavebin.PCM16BitStereoSample]{W: r.buffWriter}
	pcmLen := len(samples)
	for i := 0; i < pcmLen; i += 2 {
		_, err := pcmWriter.WriteSamples(wavebin.PCM16BitStereoSample{
			L: samples[i],
			R: samples[i+1],
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *fileRecorder) insertSilentSamples(count uint32) error {
	zr := limitedZeroReader{N: int64(pcmWaveMetaFormat.BlockAlign()) * int64(count)}
	if buffered := r.buffWriter.Buffered(); buffered > 0 {
		_, err := io.CopyN(r.buffWriter, &zr, int64(r.buffWriter.Available()))
		if err != nil {
			return err
		}
	}
	if err := r.buffWriter.Flush(); err != nil {
		return err
	}

	_, err := zr.WriteTo(r.sampleWriter)
	if err != nil {
		return err
	}

	return nil
}

func (r *fileRecorder) Close() error {
	log.Info().Msg("bufio flushing...")
	err := r.buffWriter.Flush()
	if err != nil {
		_ = r.sampleWriter.Close()
		_ = r.f.Close()
		return err
	}

	log.Info().Msg("closing sample writer...")
	err = r.sampleWriter.Close()
	if err != nil {
		_ = r.f.Close()
		return err
	}

	log.Info().Msg("closing writer...")
	return r.f.Close()
}

type limitedZeroReader struct {
	N int64
}

var (
	empty4096b [4096]byte
)

func (r *limitedZeroReader) Read(p []byte) (int, error) {
	if r.N == 0 {
		return 0, io.EOF
	}

	if r.N < int64(len(p)) {
		p = p[:r.N]
	}

	var n int
	for r.N > 0 && len(p) > 0 {
		bulkSize := 4096
		if r.N < 4096 {
			bulkSize = int(r.N)
		}
		if bulkSize > len(p) {
			bulkSize = len(p)
		}

		copy(p, empty4096b[:bulkSize])
		p = p[bulkSize:]
		n += bulkSize
	}

	return n, nil
}
func (r *limitedZeroReader) WriteTo(w io.Writer) (n int64, err error) {
	for r.N > 0 {
		var nn int
		if r.N < 4096 {
			nn, err = w.Write(empty4096b[:r.N])
			n += int64(nn)
			r.N -= int64(nn)
			if err != nil {
				return
			}
		} else {
			nn, err = w.Write(empty4096b[:])
			n += int64(nn)
			r.N -= int64(nn)
			if err != nil {
				return
			}
		}
	}

	return
}
