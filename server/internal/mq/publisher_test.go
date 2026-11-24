package mq

import (
	"io"
	"log"
	"testing"
)

func TestCheckFailureDoesNotRetainConnection(t *testing.T) {
	publisher := NewPublisher("://invalid", "pdf.import", log.New(io.Discard, "", 0))
	if err := publisher.Check(); err == nil {
		t.Fatal("expected RabbitMQ connection check to fail")
	}
	if publisher.conn != nil || publisher.ch != nil {
		t.Fatal("failed connection check must not retain connection or channel")
	}
}
