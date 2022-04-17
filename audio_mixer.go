package polsvoice

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/karupanerura/riffbin"
	"github.com/karupanerura/wavebin"
	"golang.org/x/sync/errgroup"
)

type AudioMixer struct {
	filePrefix string
}

func NewAudioMixer(filePrefix string) *AudioMixer {
	return &AudioMixer{filePrefix: filePrefix}
}

func (m *AudioMixer) Mixdown(ctx context.Context) error {
	pattern := m.filePrefix + "*.wav"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}

	completed := false
	ctx, cancel := context.WithCancel(ctx)
	g, ctx := errgroup.WithContext(ctx)
	defer func() {
		if !completed {
			cancel()     // to stop all goroutines
			_ = g.Wait() // ignore error
		}
	}()

	var sampleChannels []chan wavebin.PCM16BitStereoSample
	for _, matched := range matches {
		f, err := os.Open(matched)
		if err != nil {
			return err
		}

		ch := make(chan wavebin.PCM16BitStereoSample, 64)
		sampleChannels = append(sampleChannels, ch)

		g.Go(func() error {
			defer f.Close()
			defer close(ch)

			riffChunk, err := riffbin.ReadSections(f)
			if err != nil {
				return err
			}

			_, _, _, r, err := wavebin.ParseWaveRIFF(riffChunk, false)
			if err != nil {
				return err
			}

			pr := wavebin.NewPCMReader[wavebin.PCM16BitStereoSample](bufio.NewReader(r), wavebin.PCM16BitStereoSampleParser{})
			for {
				sample, err := pr.ReadSample()
				if errors.Is(err, io.EOF) {
					return nil
				} else if err != nil {
					return err
				}

				ch <- sample
			}
		})
	}

	f, err := os.Create(m.filePrefix + "-mix.wav")
	if err != nil {
		return err
	}

	w, err := wavebin.CreateSampleWriter(f, &wavebin.ExtendedFormatChunk{
		MetaFormat: pcmWaveMetaFormat,
	})
	if err != nil {
		_ = f.Close()
		return err
	}

	buffWriter := bufio.NewWriter(w)
	pcmWriter := wavebin.PCMWriter[wavebin.PCM16BitStereoSample]{W: buffWriter}

	availableSets := map[int]struct{}{}
	for i := range sampleChannels {
		availableSets[i] = struct{}{}
	}

	samples := make([]wavebin.PCM16BitStereoSample, len(availableSets))
	for len(availableSets) != 0 {
		// read from all available sample channels
		for i := range availableSets {
			ch := sampleChannels[i]

			var ok bool
			samples[i], ok = <-ch
			if !ok {
				delete(availableSets, i)
			}
		}
		if len(availableSets) == 0 {
			break
		}

		_, err := pcmWriter.WriteSamples(mix(samples, 3)) // -3db for all
		if err != nil {
			_ = w.Close()
			_ = f.Close()
			return err
		}
	}
	if err = buffWriter.Flush(); err != nil {
		_ = w.Close()
		_ = f.Close()
		return err
	}
	if err = w.Close(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}

	completed = true
	return g.Wait()
}

func mix(samples []wavebin.PCM16BitStereoSample, decrDb float64) (mixed wavebin.PCM16BitStereoSample) {
	if len(samples) == 0 {
		return
	}
	if len(samples) == 1 {
		return samples[0]
	}

	for _, sample := range samples {
		mixed.L = peakLimitMix16(mixed.L, int16(math.Round(float64(sample.L)*math.Pow(10.0, decrDb/20.0))))
		mixed.R = peakLimitMix16(mixed.R, int16(math.Round(float64(sample.R)*math.Pow(10.0, decrDb/20.0))))
	}
	return mixed
}

func peakLimitMix16(lhs, rhs int16) int16 {
	if lhs == 0 {
		return rhs
	} else if lhs > 0 {
		if margin := math.MaxInt16 - lhs; margin < rhs {
			return math.MaxInt16
		}
	} else { // if lhs < 0
		if margin := math.MinInt16 - lhs; margin > rhs {
			return math.MinInt16
		}
	}

	return lhs + rhs
}
