package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/broker"
)

var _ broker.Publisher = (*Publisher)(nil)

var errNacked = errors.New("rabbitmq did not confirm the message")

// Publisher публикует события в avatars.exchange с подтверждениями
// (publisher confirms): Publish возвращается, когда брокер принял сообщение.
// Если соединение порвалось, следующий Publish переподключается.
type Publisher struct {
	url    string
	logger *slog.Logger

	// mu защищает conn и ch. Публикации идут по одной: ждать подтверждения
	// под мьютексом проще, чем сопоставлять подтверждения с запросами,
	// а для потока загрузок аватарок этого хватает.
	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewPublisher подключается к RabbitMQ и объявляет топологию.
func NewPublisher(url string, logger *slog.Logger) (*Publisher, error) {
	p := &Publisher{url: url, logger: logger}
	if err := p.connect(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Publisher) connect() error {
	conn, ch, err := dial(p.url)
	if err != nil {
		return err
	}
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return fmt.Errorf("enable publisher confirms: %w", err)
	}
	p.conn, p.ch = conn, ch
	return nil
}

// Publish отправляет сообщение с routing key = msg.Type и ждёт подтверждения.
func (p *Publisher) Publish(ctx context.Context, msg broker.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ch == nil || p.ch.IsClosed() {
		p.logger.WarnContext(ctx, "rabbitmq channel is closed, reconnecting")
		if err := p.closeLocked(); err != nil {
			p.logger.WarnContext(ctx, "close broken rabbitmq connection", "err", err)
		}
		if err := p.connect(); err != nil {
			return err
		}
	}

	conf, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, broker.ExchangeAvatars, msg.Type, false, false, toPublishing(msg))
	if err != nil {
		return fmt.Errorf("publish to rabbitmq: %w", err)
	}
	acked, err := conf.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait for rabbitmq confirm: %w", err)
	}
	if !acked {
		return errNacked
	}
	return nil
}

// Ping проверяет, что соединение и канал открыты. Не переподключается,
// чтобы /health отвечал быстро.
func (p *Publisher) Ping(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn == nil || p.conn.IsClosed() || p.ch == nil || p.ch.IsClosed() {
		return errChannelClosed
	}
	return nil
}

// Close закрывает канал и соединение.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked()
}

func (p *Publisher) closeLocked() error {
	if p.conn == nil {
		return nil
	}
	err := p.conn.Close()
	p.conn, p.ch = nil, nil
	if errors.Is(err, amqp.ErrClosed) {
		return nil
	}
	return err
}
