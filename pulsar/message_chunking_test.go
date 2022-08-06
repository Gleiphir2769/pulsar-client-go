package pulsar

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var _brokerMaxMessageSize = 1024 * 1024

func TestInvalidChunkingConfig(t *testing.T) {
	client, err := NewClient(ClientOptions{
		URL: lookupURL,
	})

	assert.Nil(t, err)
	defer client.Close()

	// create producer
	producer, err := client.CreateProducer(ProducerOptions{
		Topic:           newTopicName(),
		DisableBatching: false,
		EnableChunking:  true,
	})

	assert.Error(t, err, "producer creation should have fail")
	assert.Nil(t, producer)
}

func TestLargeMessage(t *testing.T) {
	rand.Seed(time.Now().Unix())

	client, err := NewClient(ClientOptions{
		URL: lookupURL,
	})

	assert.Nil(t, err)
	defer client.Close()

	topic := newTopicName()

	// create producer without ChunkMaxMessageSize
	producer1, err := client.CreateProducer(ProducerOptions{
		Topic:           topic,
		DisableBatching: true,
		EnableChunking:  true,
	})
	assert.NoError(t, err)
	assert.NotNil(t, producer1)
	defer producer1.Close()

	// create producer with ChunkMaxMessageSize
	producer2, err := client.CreateProducer(ProducerOptions{
		Topic:               topic,
		DisableBatching:     true,
		EnableChunking:      true,
		ChunkMaxMessageSize: 5,
	})
	assert.NoError(t, err)
	assert.NotNil(t, producer2)
	defer producer2.Close()

	consumer, err := client.Subscribe(ConsumerOptions{
		Topic:            topic,
		Type:             Exclusive,
		SubscriptionName: "chunk-subscriber",
	})
	assert.NoError(t, err)
	assert.NotNil(t, consumer)
	defer consumer.Close()

	expectMsgs := make([][]byte, 0, 10)

	// test send chunk with serverMaxMessageSize limit
	for i := 0; i < 5; i++ {
		msg := createTestMessagePayload(_brokerMaxMessageSize + 1)
		expectMsgs = append(expectMsgs, msg)
		ID, err := producer1.Send(context.Background(), &ProducerMessage{
			Payload: msg,
		})
		assert.NoError(t, err)
		assert.NotNil(t, ID)
	}

	// test receive chunk with serverMaxMessageSize limit
	for i := 0; i < 5; i++ {
		msg, err := consumer.Receive(context.Background())
		assert.NoError(t, err)

		expectMsg := expectMsgs[i]

		assert.Equal(t, expectMsg, msg.Payload())
		// ack message
		err = consumer.Ack(msg)
		assert.NoError(t, err)
	}

	// test send chunk with ChunkMaxMessageSize limit
	for i := 0; i < 5; i++ {
		msg := createTestMessagePayload(50)
		expectMsgs = append(expectMsgs, msg)
		ID, err := producer2.Send(context.Background(), &ProducerMessage{
			Payload: msg,
		})
		assert.NoError(t, err)
		assert.NotNil(t, ID)
	}

	// test receive chunk with ChunkMaxMessageSize limit
	for i := 5; i < 10; i++ {
		msg, err := consumer.Receive(context.Background())
		assert.NoError(t, err)

		expectMsg := expectMsgs[i]

		assert.Equal(t, expectMsg, msg.Payload())
		// ack message
		err = consumer.Ack(msg)
		assert.NoError(t, err)
	}
}

func TestPublishChunkWithFailure(t *testing.T) {
	rand.Seed(time.Now().Unix())

	client, err := NewClient(ClientOptions{
		URL: lookupURL,
	})

	assert.Nil(t, err)
	defer client.Close()

	topic := newTopicName()

	// create producer without ChunkMaxMessageSize
	producer, err := client.CreateProducer(ProducerOptions{
		Topic: topic,
	})
	assert.NoError(t, err)
	assert.NotNil(t, producer)
	defer producer.Close()

	ID, err := producer.Send(context.Background(), &ProducerMessage{
		Payload: createTestMessagePayload(_brokerMaxMessageSize + 1),
	})
	assert.Error(t, err)
	assert.Nil(t, ID)
}

func TestMaxPendingChunkMessages(t *testing.T) {
	rand.Seed(time.Now().Unix())

	client, err := NewClient(ClientOptions{
		URL: lookupURL,
	})
	assert.Nil(t, err)
	defer client.Close()

	topic := newTopicName()

	totalProducers := 5
	producers := make([]Producer, 0, 20)
	defer func() {
		for _, p := range producers {
			p.Close()
		}
	}()

	clients := make([]Client, 0, 20)
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	for j := 0; j < totalProducers; j++ {
		pc, err := NewClient(ClientOptions{
			URL: lookupURL,
		})
		assert.Nil(t, err)
		clients = append(clients, pc)
		producer, err := pc.CreateProducer(ProducerOptions{
			Topic:               topic,
			DisableBatching:     true,
			EnableChunking:      true,
			ChunkMaxMessageSize: 10,
		})
		assert.NoError(t, err)
		assert.NotNil(t, producer)
		producers = append(producers, producer)
	}

	consumer, err := client.Subscribe(ConsumerOptions{
		Topic:                    topic,
		Type:                     Exclusive,
		SubscriptionName:         "chunk-subscriber",
		MaxPendingChunkedMessage: 1,
	})
	assert.NoError(t, err)
	assert.NotNil(t, consumer)
	defer consumer.Close()

	totalMsgs := 40
	wg := sync.WaitGroup{}
	wg.Add(totalMsgs * totalProducers)
	for i := 0; i < totalMsgs; i++ {
		for j := 0; j < totalProducers; j++ {
			p := producers[j]
			go func() {
				ID, err := p.Send(context.Background(), &ProducerMessage{
					Payload: createTestMessagePayload(50),
				})
				assert.NoError(t, err)
				assert.NotNil(t, ID)
				wg.Done()
			}()
		}
	}
	wg.Wait()

	received := 0
	for i := 0; i < totalMsgs*totalProducers; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		msg, err := consumer.Receive(ctx)
		cancel()
		if msg == nil || (err != nil && errors.Is(err, context.DeadlineExceeded)) {
			break
		}

		received++

		err = consumer.Ack(msg)
		assert.NoError(t, err)
	}

	assert.NotEqual(t, totalMsgs*totalProducers, received)
}

func TestExpireIncompleteChunks(t *testing.T) {
	rand.Seed(time.Now().Unix())
	client, err := NewClient(ClientOptions{
		URL: lookupURL,
	})

	assert.Nil(t, err)
	defer client.Close()

	topic := newTopicName()

	c, err := client.Subscribe(ConsumerOptions{
		Topic:                       topic,
		Type:                        Exclusive,
		SubscriptionName:            "chunk-subscriber",
		ExpireTimeOfIncompleteChunk: time.Millisecond * 300,
	})
	assert.NoError(t, err)
	defer c.Close()

	uuid := "test-uuid"
	chunkCtxMap := c.(*consumer).consumers[0].chunkedMsgCtxMap
	chunkCtxMap.addIfAbsent(uuid, 2, 100)
	ctx := chunkCtxMap.get(uuid)
	assert.NotNil(t, ctx)

	time.Sleep(400 * time.Millisecond)

	ctx = chunkCtxMap.get(uuid)
	assert.Nil(t, ctx)
}

func TestChunksEnqueueFailed(t *testing.T) {
	rand.Seed(time.Now().Unix())

	client, err := NewClient(ClientOptions{
		URL: lookupURL,
	})

	assert.Nil(t, err)
	defer client.Close()

	topic := newTopicName()

	producer, err := client.CreateProducer(ProducerOptions{
		Topic:                   topic,
		EnableChunking:          true,
		DisableBatching:         true,
		MaxPendingMessages:      10,
		ChunkMaxMessageSize:     50,
		DisableBlockIfQueueFull: true,
	})
	assert.NoError(t, err)
	assert.NotNil(t, producer)
	defer producer.Close()

	ID, err := producer.Send(context.Background(), &ProducerMessage{
		Payload: createTestMessagePayload(1000),
	})
	assert.Error(t, err)
	assert.Nil(t, ID)
}

func createTestMessagePayload(size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(rand.Intn(100))
	}
	return payload
}
