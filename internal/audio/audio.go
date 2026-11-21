// Package audio wraps miniaudio (via malgo) to capture the microphone and
// play the incoming stream, both on independently selectable devices.
//
// Fixed audio format everywhere: mono, 48 kHz, s16le — raw PCM goes over
// the network uncompressed, so "отличное качество" is the codec's default.
package audio

import (
	"encoding/binary"
	"sync"
	"unsafe"

	"github.com/gen2brain/malgo"
)

const (
	SampleRate   = 48000
	FrameSamples = 480 // 10 мс моно

	captureRingCap  = SampleRate // 1 s
	jitterRingCap   = SampleRate / 2
	jitterPrebuffer = FrameSamples * 3 // ждём 30 мс до начала воспроизведения
)

type DeviceKind int

const (
	KindCapture DeviceKind = iota
	KindPlayback
)

// Device is one audio endpoint as reported by the OS.
type Device struct {
	Name      string
	IsDefault bool

	id unsafe.Pointer // жив, пока жив контекст malgo
}

// Engine owns the malgo context, the two ring buffers and the devices.
type Engine struct {
	ctx *malgo.AllocatedContext

	mu      sync.Mutex
	micVol  float64 // 0..2
	spkVol  float64 // 0..2
	micMute bool

	micRing  *ring // захват -> сеть
	jitRing  *ring // сеть -> воспроизведение (джиттер-буфер)
	starving bool
	scratch  []int16

	capDev   *malgo.Device
	playDev  *malgo.Device
	capList  []Device
	playList []Device
}

func NewEngine() (*Engine, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, err
	}
	return &Engine{
		ctx:     ctx,
		micRing: newRing(captureRingCap),
		jitRing: newRing(jitterRingCap),
		scratch: make([]int16, FrameSamples*4),
		micVol:  1,
		spkVol:  1,
	}, nil
}

// Close stops devices and releases the audio context.
func (e *Engine) Close() {
	e.StopDevices()
	_ = e.ctx.Uninit()
}

// Devices lists capture or playback endpoints and caches them: device ids
// from this list are what the *ByName methods below switch to.
func (e *Engine) Devices(kind DeviceKind) ([]Device, error) {
	t := malgo.Playback
	if kind == KindCapture {
		t = malgo.Capture
	}
	infos, err := e.ctx.Devices(t)
	if err != nil {
		return nil, err
	}
	list := make([]Device, 0, len(infos))
	for i := range infos {
		list = append(list, Device{
			Name:      infos[i].Name(),
			IsDefault: infos[i].IsDefault != 0,
			// Указатель на id внутри infos: malgo копирует идентификатор
			// при InitDevice, а срез остаётся жить в e.capList/e.playList.
			id: unsafe.Pointer(&infos[i].ID),
		})
	}
	if kind == KindCapture {
		e.capList = list
	} else {
		e.playList = list
	}
	return list, nil
}

// UseCaptureByName switches the microphone. Empty name = default device.
func (e *Engine) UseCaptureByName(name string) error {
	id, err := e.findID(e.capList, name)
	if err != nil {
		return err
	}
	return e.StartCapture(id)
}

// UsePlaybackByName switches the output device. Empty name = default.
func (e *Engine) UsePlaybackByName(name string) error {
	id, err := e.findID(e.playList, name)
	if err != nil {
		return err
	}
	return e.StartPlayback(id)
}

func (e *Engine) findID(list []Device, name string) (unsafe.Pointer, error) {
	for i := range list {
		if list[i].Name == name {
			return list[i].id, nil
		}
	}
	if len(list) > 0 {
		// Устройство могло пропасть (переподключили гарнитуру) — берём дефолт.
		for i := range list {
			if list[i].IsDefault {
				return list[i].id, nil
			}
		}
		return list[0].id, nil
	}
	return nil, errNoDevices
}

// StartCapture opens the microphone (nil id = default device).
func (e *Engine) StartCapture(deviceID unsafe.Pointer) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.capDev != nil {
		e.capDev.Uninit()
		e.capDev = nil
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.SampleRate = SampleRate
	cfg.PeriodSizeInMilliseconds = 10
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.Capture.DeviceID = deviceID

	dev, err := malgo.InitDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{Data: e.onCaptureData})
	if err != nil {
		return err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return err
	}
	e.capDev = dev
	return nil
}

// StartPlayback opens the output device (nil id = default device).
func (e *Engine) StartPlayback(deviceID unsafe.Pointer) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.playDev != nil {
		e.playDev.Uninit()
		e.playDev = nil
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.SampleRate = SampleRate
	cfg.PeriodSizeInMilliseconds = 10
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = 1
	cfg.Playback.DeviceID = deviceID

	dev, err := malgo.InitDevice(e.ctx.Context, cfg, malgo.DeviceCallbacks{Data: e.onPlaybackData})
	if err != nil {
		return err
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return err
	}
	e.playDev = dev
	return nil
}

// StopDevices releases the mic and speakers (mic indicator light goes off).
func (e *Engine) StopDevices() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.capDev != nil {
		e.capDev.Uninit()
		e.capDev = nil
	}
	if e.playDev != nil {
		e.playDev.Uninit()
		e.playDev = nil
	}
}

// WriteReceived pushes a decoded frame into the jitter buffer.
func (e *Engine) WriteReceived(frame []int16) {
	e.jitRing.write(frame)
}

// ReadMicFrame pulls exactly FrameSamples captured samples into dst,
// applying mic volume and mute. Returns false if a full frame isn't there.
func (e *Engine) ReadMicFrame(dst []int16) bool {
	if len(dst) < FrameSamples {
		return false
	}
	n := e.micRing.read(dst[:FrameSamples])
	if n < FrameSamples {
		return false
	}

	e.mu.Lock()
	mute, vol := e.micMute, e.micVol
	e.mu.Unlock()

	if mute || vol == 0 {
		for i := 0; i < FrameSamples; i++ {
			dst[i] = 0
		}
		return true
	}
	if vol != 1 {
		for i := 0; i < FrameSamples; i++ {
			dst[i] = clampSample(int(float64(dst[i]) * vol))
		}
	}
	return true
}

func (e *Engine) SetMicVolume(v float64)  { e.mu.Lock(); e.micVol = clampVol(v); e.mu.Unlock() }
func (e *Engine) SetSpkVolume(v float64)  { e.mu.Lock(); e.spkVol = clampVol(v); e.mu.Unlock() }
func (e *Engine) SetMicMuted(muted bool)  { e.mu.Lock(); e.micMute = muted; e.mu.Unlock() }

// onCaptureData runs on the device thread: copy mic bytes into the ring.
func (e *Engine) onCaptureData(_, input []byte, _ uint32) {
	n := len(input) / 2
	if n == 0 {
		return
	}
	tmp := make([]int16, n)
	for i := 0; i < n; i++ {
		tmp[i] = int16(binary.LittleEndian.Uint16(input[i*2:]))
	}
	e.micRing.write(tmp)
}

// onPlaybackData runs on the device thread: pull from the jitter buffer,
// apply volume; on underrun output silence and wait for the prebuffer.
func (e *Engine) onPlaybackData(output, _ []byte, _ uint32) {
	for i := range output {
		output[i] = 0
	}
	need := len(output) / 2
	if need == 0 {
		return
	}

	e.mu.Lock()
	vol := e.spkVol
	e.mu.Unlock()

	if e.starving {
		if e.jitRing.available() < jitterPrebuffer {
			return
		}
		e.starving = false
	}

	n := e.jitRing.read(e.scratch[:need])
	if n < need {
		e.starving = true // недобор — выдаём тишину, копим предбуфер
	}
	for i := 0; i < n; i++ {
		v := clampSample(int(float64(e.scratch[i]) * vol))
		binary.LittleEndian.PutUint16(output[i*2:], uint16(v))
	}
}

func clampVol(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 2 {
		return 2
	}
	return v
}

func clampSample(v int) int16 {
	if v < -32768 {
		return -32768
	}
	if v > 32767 {
		return 32767
	}
	return int16(v)
}

var errNoDevices = errStr("аудиоустройства не найдены")

type errStr string

func (e errStr) Error() string { return string(e) }
