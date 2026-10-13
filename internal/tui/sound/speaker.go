package sound

import (
	"errors"
	"fmt"
	"time"

	"github.com/ebitengine/oto/v3"
)

// latency is how much sound is queued ahead in each of the device and the
// player: enough to play without gaps, little enough that the voice stays
// with the lips.
const latency = 25 * time.Millisecond

// Speaker plays a Synth through the default audio device.
type Speaker struct {
	player *oto.Player
}

// Play starts s sounding through the default audio device, and keeps it
// sounding until the returned Speaker is closed.
func Play(s *Synth) (*Speaker, error) {
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   SampleRate,
		ChannelCount: 1,
		Format:       oto.FormatFloat32LE,
		BufferSize:   latency,
	})
	if err != nil {
		return nil, fmt.Errorf("open the audio device: %w", err)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		return nil, errors.New("audio device did not start")
	}
	player := ctx.NewPlayer(s)
	player.SetBufferSize(int(latency.Seconds()*SampleRate) * 4) // bytes: four to a sample
	player.Play()
	return &Speaker{player}, nil
}

// Close stops the sound. The device is the process's, and goes with it.
func (s *Speaker) Close() error {
	s.player.Pause()
	return nil
}
