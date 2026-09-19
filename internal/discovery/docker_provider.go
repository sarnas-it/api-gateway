package discovery

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// initialBackoff — базовая задержка переподключения к потоку событий.
	initialBackoff = time.Second
	// maxBackoff — верхняя граница экспоненциальной задержки.
	maxBackoff = 30 * time.Second
	// healthyStreamDuration — минимальное время жизни потока, после которого
	// он считается здоровым и backoff сбрасывается.
	healthyStreamDuration = 30 * time.Second
)

// dockerProvider отслеживает контейнеры через Docker/Podman API и вызывает
// onResult при старте, по событиям (с дебаунсом) и по периодическому ре-синку.
type dockerProvider struct {
	client   *dockerClient
	opts     ParseOptions
	debounce time.Duration
	resync   time.Duration
	log      *zap.Logger

	// initialBackoff — базовая задержка переподключения (поле для тестов).
	initialBackoff time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped bool
	wg      sync.WaitGroup
}

// dockerProvider реализует Provider.
var _ Provider = (*dockerProvider)(nil)

// NewDockerProvider создаёт Docker-провайдер. Обёртка над newDockerProvider,
// экспортированная для использования плагинами discovery.
func NewDockerProvider(host, apiVersion string, opts ParseOptions, debounce, resync time.Duration, log *zap.Logger) (Provider, error) {
	return newDockerProvider(host, apiVersion, opts, debounce, resync, log)
}

func newDockerProvider(host, apiVersion string, opts ParseOptions, debounce, resync time.Duration, log *zap.Logger) (*dockerProvider, error) {
	client, err := newDockerClient(host, apiVersion)
	if err != nil {
		return nil, err
	}
	return &dockerProvider{
		client:         client,
		opts:           opts,
		debounce:       debounce,
		resync:         resync,
		log:            log,
		initialBackoff: initialBackoff,
	}, nil
}

// Start запускает отслеживание. Возвращает управление после отмены ctx или
// после Stop; к моменту возврата все горутины завершены, поэтому onResult
// больше не вызывается.
func (p *dockerProvider) Start(ctx context.Context, onResult func(Result)) error {
	ctx, cancel := context.WithCancel(ctx)

	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		cancel()
		return nil
	}
	p.cancel = cancel
	// Резервируем счётчики для цикла Start и наблюдателя событий до запуска
	// горутин, чтобы Stop не начал Wait раньше Add.
	p.wg.Add(2)
	p.mu.Unlock()

	defer p.wg.Done()

	p.sync(ctx, onResult)

	triggers := make(chan struct{}, 1)
	go func() {
		defer p.wg.Done()
		p.watchEvents(ctx, triggers)
	}()

	var tickerC <-chan time.Time
	if p.resync > 0 {
		ticker := time.NewTicker(p.resync)
		defer ticker.Stop()
		tickerC = ticker.C
	}

	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopTimer()
			return nil
		case <-triggers:
			stopTimer()
			timer = time.NewTimer(p.debounce)
			timerC = timer.C
		case <-timerC:
			stopTimer()
			p.sync(ctx, onResult)
		case <-tickerC:
			p.sync(ctx, onResult)
		}
	}
}

func (p *dockerProvider) sync(ctx context.Context, onResult func(Result)) {
	containers, err := p.client.listContainers(ctx, p.opts.LabelPrefix)
	if err != nil {
		p.log.Warn("discovery: list containers failed", zap.Error(err))
		return
	}
	onResult(ParseContainers(containers, p.opts))
}

// watchEvents подписывается на /events и шлёт триггер в triggers. При обрыве
// потока (закрытии или ошибке подписки) переподключается с экспоненциальным
// backoff до отмены ctx и сигнализирует о необходимости ре-синка: обрыв
// трактуется как reconnect-and-resync. Backoff сбрасывается только после
// здорового потока, чтобы пир, принимающий и сразу закрывающий соединение, не
// вызывал реконнект каждую секунду бесконечно.
func (p *dockerProvider) watchEvents(ctx context.Context, triggers chan<- struct{}) {
	notify := func() {
		select {
		case triggers <- struct{}{}:
		default:
		}
	}
	backoff := p.initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		ch, err := p.client.events(ctx)
		if err != nil {
			p.log.Warn("discovery: events subscribe failed", zap.Error(err))
			notify()
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = growBackoff(backoff)
			continue
		}
		events := 0
		for action := range ch {
			events++
			p.log.Debug("discovery: container event", zap.String("action", action))
			notify()
		}
		if streamHealthy(events, started, time.Now()) {
			backoff = p.initialBackoff
		}
		notify()
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = growBackoff(backoff)
	}
}

// growBackoff удваивает задержку переподключения, ограничивая её maxBackoff.
func growBackoff(prev time.Duration) time.Duration {
	if next := prev * 2; next < maxBackoff {
		return next
	}
	return maxBackoff
}

// streamHealthy сообщает, был ли поток событий здоров: он либо доставил хотя
// бы одно событие, либо прожил не меньше healthyStreamDuration.
func streamHealthy(events int, started, now time.Time) bool {
	return events > 0 || now.Sub(started) >= healthyStreamDuration
}

// Stop отменяет контекст и ждёт завершения Start и наблюдателя событий, после
// чего onResult гарантированно больше не вызывается.
func (p *dockerProvider) Stop() error {
	p.mu.Lock()
	p.stopped = true
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mu.Unlock()

	p.wg.Wait()
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
