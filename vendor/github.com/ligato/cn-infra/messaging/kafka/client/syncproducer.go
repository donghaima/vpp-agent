// Copyright (c) 2017 Cisco and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"crypto/md5"
	"errors"
	"fmt"
	"sync"

	"github.com/Shopify/sarama"
	"github.com/ligato/cn-infra/logging"
)

// SyncProducer allows to publish messages to kafka using synchronous API.
type SyncProducer struct {
	logging.Logger
	Config       *Config
	Client       sarama.Client
	Producer     sarama.SyncProducer
	Partition    int32
	closed       bool
	xwg          *sync.WaitGroup
	closeChannel chan struct{}
	sync.Mutex
}

// NewSyncProducer returns a new SyncProducer
func NewSyncProducer(config *Config, wg *sync.WaitGroup) (*SyncProducer, error) {
	if config.Debug {
		config.Logger.SetLevel(logging.DebugLevel)
	}

	config.Logger.Debug("entering NewSyncProducer ...")
	if err := config.ValidateSyncProducerConfig(); err != nil {
		return nil, err
	}

	// set "RequiredAcks" for producer
	if config.RequiredAcks == AcksUnset {
		config.RequiredAcks = WaitForAll
	}
	err := setProducerRequiredAcks(config)
	if err != nil {
		return nil, errors.New("invalid RequiredAcks field in config")
	}

	// set other Producer config params
	config.ProducerConfig().Producer.Partitioner = config.Partitioner
	config.ProducerConfig().Producer.Return.Successes = true

	config.Logger.Debugf("SyncProducer config: %#v", config)

	// init a new client
	client, err := sarama.NewClient(config.Brokers, &config.Config.Config)
	if err != nil {
		return nil, err
	}

	producer, err := sarama.NewSyncProducerFromClient(client)
	if err != nil {
		return nil, err
	}

	// initProducer object
	sp := &SyncProducer{
		Logger:       config.Logger,
		Config:       config,
		Client:       client,
		Producer:     producer,
		Partition:    config.Partition,
		closed:       false,
		closeChannel: make(chan struct{}),
	}

	// if there is a "waitgroup" arg then use it
	if wg != nil {
		sp.xwg = wg
		sp.xwg.Add(1)
	}

	return sp, nil
}

// Close closes the client and producer
func (ref *SyncProducer) Close() error {
	defer func() {
		if ref.closed {
			ref.Unlock()
			return
		}
		ref.closed = true
		close(ref.closeChannel)

		// decrement external waitgroup
		if ref.xwg != nil {
			ref.xwg.Done()
		}

		ref.Unlock()
	}()

	ref.Lock()
	if ref.closed {
		return nil
	}

	err := ref.Producer.Close()
	if err != nil {
		ref.Errorf("SyncProducer close error: %v", err)
		return err
	}
	ref.Debug("SyncProducer closed")

	err = ref.Client.Close()
	if err != nil {
		ref.Errorf("client close error: %v", err)
		return err
	}

	return nil
}

// SendMsgByte sends a message to Kafka
func (ref *SyncProducer) SendMsgByte(topic string, key []byte, msg []byte) (*ProducerMessage, error) {
	// generate a key if none supplied (used by hash partitioner)
	ref.WithFields(logging.Fields{"key": key, "msg": msg}).Debug("Sending")

	if key == nil || len(key) == 0 {
		md5Sum := fmt.Sprintf("%x", md5.Sum(msg))
		return ref.SendMsg(topic, sarama.ByteEncoder(md5Sum), sarama.ByteEncoder(msg))
	}
	return ref.SendMsg(topic, sarama.ByteEncoder(key), sarama.ByteEncoder(msg))
}

// SendMsg sends a message to Kafka
func (ref *SyncProducer) SendMsg(topic string, key sarama.Encoder, msg sarama.Encoder) (*ProducerMessage, error) {
	if msg == nil {
		err := errors.New("nil message can not be sent")
		ref.Error(err)
		return nil, err
	}
	message := &sarama.ProducerMessage{
		Topic: topic,
		Value: msg,
		Key:   key,
	}

	// manual partition
	if ref.Partition > -1 {
		message.Partition = ref.Partition
	}

	partition, offset, err := ref.Producer.SendMessage(message)
	pmsg := &ProducerMessage{
		Topic:     message.Topic,
		Key:       message.Key,
		Value:     message.Value,
		Metadata:  message.Metadata,
		Offset:    offset,
		Partition: partition,
	}
	if err != nil {
		ref.Errorf("message error: %s, err: %v", pmsg, err)
		return pmsg, err
	}

	ref.Debugf("message sent: %s", pmsg)
	return pmsg, nil
}

// setProducerRequiredAcks set the RequiredAcks field for a producer
func setProducerRequiredAcks(cfg *Config) error {
	switch cfg.RequiredAcks {
	case NoResponse:
		cfg.ProducerConfig().Producer.RequiredAcks = sarama.NoResponse
		return nil
	case WaitForLocal:
		cfg.ProducerConfig().Producer.RequiredAcks = sarama.WaitForLocal
		return nil
	case WaitForAll:
		cfg.ProducerConfig().Producer.RequiredAcks = sarama.WaitForAll
		return nil
	default:
		return errors.New("Invalid RequiredAcks type")
	}
}

// IsClosed returns the "closed" status
func (ref *SyncProducer) IsClosed() bool {
	ref.Lock()
	defer ref.Unlock()

	return ref.closed
}

// WaitForClose returns when the producer is closed
func (ref *SyncProducer) WaitForClose() {
	<-ref.closeChannel
}

// GetCloseChannel returns a channel that is closed on asyncProducer cleanup
func (ref *SyncProducer) GetCloseChannel() <-chan struct{} {
	return ref.closeChannel
}
