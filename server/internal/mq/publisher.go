// Package mq 封装 RabbitMQ 的连接与消息投递，连接断开时按需重连。
package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Publisher 向固定队列投递持久化 JSON 消息。
type Publisher struct {
	url    string
	queue  string
	logger *log.Logger

	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewPublisher 创建发布器；此时不建立连接，首次投递时再连。
func NewPublisher(url, queue string, logger *log.Logger) *Publisher {
	return &Publisher{url: url, queue: queue, logger: logger}
}

// Publish 投递一条 JSON 消息。连接失效时自动重连一次。
func (p *Publisher) Publish(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	var publishErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := p.ensureLocked(); err != nil {
			p.closeLocked()
			publishErr = err
			continue
		}
		publishErr = p.ch.PublishWithContext(ctx, "", p.queue, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         body,
		})
		if publishErr == nil {
			return nil
		}
		p.closeLocked()
	}
	return fmt.Errorf("publish to %s: %w", p.queue, publishErr)
}

// Close 释放连接。
func (p *Publisher) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeLocked()
}

func (p *Publisher) ensureLocked() error {
	if p.conn != nil && !p.conn.IsClosed() && p.ch != nil {
		return nil
	}
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("open channel: %w", err)
	}
	if _, err := ch.QueueDeclare(p.queue, true, false, false, false, nil); err != nil {
		ch.Close()
		conn.Close()
		return fmt.Errorf("declare queue %s: %w", p.queue, err)
	}
	p.conn = conn
	p.ch = ch
	p.logger.Printf("rabbitmq connected, queue=%s", p.queue)
	return nil
}

func (p *Publisher) closeLocked() {
	if p.ch != nil {
		_ = p.ch.Close()
		p.ch = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
}
